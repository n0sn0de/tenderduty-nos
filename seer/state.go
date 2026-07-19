package seer

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

const (
	currentStateVersion = 1
	stateBackupSuffix   = ".bak"
)

type stateDocument struct {
	Version   int                             `json:"version,omitempty"`
	Alarms    *alarmCache                     `json:"alarms"`
	Blocks    map[string][]int                `json:"blocks"`
	NodesDown map[string]map[string]time.Time `json:"nodes_down"`
}

type stateLoadInfo struct {
	Source              string
	Version             int
	Legacy              bool
	RecoveredFromBackup bool
}

type stateTempFile interface {
	io.Writer
	Chmod(fs.FileMode) error
	Sync() error
	Close() error
	Name() string
}

type stateFileOps struct {
	readFile      func(string) ([]byte, error)
	createTemp    func(string, string) (stateTempFile, error)
	rename        func(string, string) error
	remove        func(string) error
	syncDirectory func(string) error
}

func defaultStateFileOps() stateFileOps {
	return stateFileOps{
		readFile: os.ReadFile,
		createTemp: func(directory, pattern string) (stateTempFile, error) {
			return os.CreateTemp(directory, pattern)
		},
		rename:        os.Rename,
		remove:        os.Remove,
		syncDirectory: syncStateDirectory,
	}
}

func decodeState(data []byte) (*savedState, stateLoadInfo, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, stateLoadInfo{}, errors.New("state file is empty")
	}

	var document *stateDocument
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&document); err != nil {
		return nil, stateLoadInfo{}, fmt.Errorf("decode state JSON: %w", err)
	}
	if document == nil {
		return nil, stateLoadInfo{}, errors.New("state document is null")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, stateLoadInfo{}, errors.New("state file contains multiple JSON values")
		}
		return nil, stateLoadInfo{}, fmt.Errorf("decode trailing state data: %w", err)
	}
	if document.Version < 0 || document.Version > currentStateVersion {
		return nil, stateLoadInfo{}, fmt.Errorf("unsupported state version %d", document.Version)
	}
	if document.Alarms == nil && document.Blocks == nil && document.NodesDown == nil {
		return nil, stateLoadInfo{}, errors.New("state document contains no durable state fields")
	}

	return &savedState{
			Alarms:    document.Alarms,
			Blocks:    document.Blocks,
			NodesDown: document.NodesDown,
		}, stateLoadInfo{
			Version: document.Version,
			Legacy:  document.Version == 0,
		}, nil
}

func loadState(statePath string) (*savedState, stateLoadInfo, error) {
	return loadStateWithReader(statePath, os.ReadFile)
}

func loadStateWithReader(statePath string, readFile func(string) ([]byte, error)) (*savedState, stateLoadInfo, error) {
	state, info, primaryErr := readStateCandidate(statePath, readFile)
	if primaryErr == nil {
		info.Source = statePath
		return state, info, nil
	}

	backupPath := statePath + stateBackupSuffix
	backup, backupInfo, backupErr := readStateCandidate(backupPath, readFile)
	if backupErr == nil {
		backupInfo.Source = backupPath
		backupInfo.RecoveredFromBackup = true
		return backup, backupInfo, nil
	}

	if errors.Is(primaryErr, os.ErrNotExist) && errors.Is(backupErr, os.ErrNotExist) {
		return &savedState{}, stateLoadInfo{}, nil
	}
	if errors.Is(primaryErr, os.ErrNotExist) {
		return nil, stateLoadInfo{}, fmt.Errorf("read state backup %q: %w", backupPath, backupErr)
	}
	primaryWrapped := fmt.Errorf("read state %q: %w", statePath, primaryErr)
	if errors.Is(backupErr, os.ErrNotExist) {
		return nil, stateLoadInfo{}, primaryWrapped
	}
	return nil, stateLoadInfo{}, errors.Join(
		primaryWrapped,
		fmt.Errorf("read state backup %q: %w", backupPath, backupErr),
	)
}

func readStateCandidate(statePath string, readFile func(string) ([]byte, error)) (*savedState, stateLoadInfo, error) {
	data, err := readFile(statePath)
	if err != nil {
		return nil, stateLoadInfo{}, err
	}
	return decodeState(data)
}

func restoreSavedState(config *Config, saved *savedState) {
	if saved == nil {
		return
	}
	for chainName, blocks := range saved.Blocks {
		if config.Chains[chainName] != nil {
			config.Chains[chainName].blocksResults = blocks
		}
	}

	// Restore accepted-delivery and dashboard state to preserve restart deduplication.
	if saved.Alarms != nil {
		if saved.Alarms.SentTgAlarms != nil {
			alarms.SentTgAlarms = saved.Alarms.SentTgAlarms
			clearStale(alarms.SentTgAlarms, "telegram", config.Pagerduty.Enabled, staleHours)
		}
		if saved.Alarms.SentPdAlarms != nil {
			alarms.SentPdAlarms = saved.Alarms.SentPdAlarms
			clearStale(alarms.SentPdAlarms, "PagerDuty", config.Pagerduty.Enabled, staleHours)
		}
		if saved.Alarms.SentDiAlarms != nil {
			alarms.SentDiAlarms = saved.Alarms.SentDiAlarms
			clearStale(alarms.SentDiAlarms, "Discord", config.Pagerduty.Enabled, staleHours)
		}
		if saved.Alarms.SentSlkAlarms != nil {
			alarms.SentSlkAlarms = saved.Alarms.SentSlkAlarms
			clearStale(alarms.SentSlkAlarms, "Slack", config.Pagerduty.Enabled, staleHours)
		}
		if saved.Alarms.AllAlarms != nil {
			alarms.AllAlarms = saved.Alarms.AllAlarms
			for _, chainAlarms := range saved.Alarms.AllAlarms {
				clearStale(chainAlarms, "dashboard", config.Pagerduty.Enabled, staleHours)
			}
		}
	}

	if saved.NodesDown == nil {
		return
	}
	for chainName, nodes := range saved.NodesDown {
		chain := config.Chains[chainName]
		if chain == nil {
			continue
		}
		for nodeURL, downSince := range nodes {
			if downSince.IsZero() {
				continue
			}
			for _, node := range chain.Nodes {
				if node.Url == nodeURL {
					node.down = true
					node.wasDown = true
					node.downSince = downSince
				}
			}
		}
	}
	for _, chain := range config.Chains {
		downCount := 0
		for _, node := range chain.Nodes {
			if node.down {
				downCount++
			}
		}
		if downCount == len(chain.Nodes) {
			chain.noNodes = true
		}
	}
}

func snapshotSavedState(config *Config) *savedState {
	config.chainsMux.RLock()
	defer config.chainsMux.RUnlock()

	blocks := make(map[string][]int)
	nodesDown := make(map[string]map[string]time.Time)
	for chainName, chain := range config.Chains {
		if config.EnableDash {
			blocks[chainName] = append([]int(nil), chain.blocksResults...)
		}
		for _, node := range chain.Nodes {
			if !node.down {
				continue
			}
			if nodesDown[chainName] == nil {
				nodesDown[chainName] = make(map[string]time.Time)
			}
			nodesDown[chainName][node.Url] = node.downSince
		}
	}
	return &savedState{
		Alarms:    alarms,
		Blocks:    blocks,
		NodesDown: nodesDown,
	}
}

func writeStateAtomic(statePath string, state *savedState) error {
	return writeStateAtomicWithOps(statePath, state, defaultStateFileOps())
}

func writeStateAtomicWithOps(statePath string, state *savedState, operations stateFileOps) error {
	if state == nil {
		return errors.New("cannot write nil state")
	}
	document := stateDocument{
		Version:   currentStateVersion,
		Alarms:    state.Alarms,
		Blocks:    state.Blocks,
		NodesDown: state.NodesDown,
	}

	current, readErr := operations.readFile(statePath)
	switch {
	case readErr == nil:
		if _, _, decodeErr := decodeState(current); decodeErr == nil {
			if err := atomicReplaceBytes(statePath+stateBackupSuffix, current, operations); err != nil {
				return fmt.Errorf("replace state backup: %w", err)
			}
		}
	case !errors.Is(readErr, os.ErrNotExist):
		return fmt.Errorf("read current state before replacement: %w", readErr)
	}

	if err := atomicReplaceJSON(statePath, document, operations); err != nil {
		return fmt.Errorf("replace state: %w", err)
	}
	return nil
}

func atomicReplaceJSON(target string, document stateDocument, operations stateFileOps) error {
	return atomicReplace(target, operations, func(writer io.Writer) error {
		if err := json.NewEncoder(writer).Encode(&document); err != nil {
			return fmt.Errorf("encode state: %w", err)
		}
		return nil
	})
}

func atomicReplaceBytes(target string, data []byte, operations stateFileOps) error {
	return atomicReplace(target, operations, func(writer io.Writer) error {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written != len(data) {
			return io.ErrShortWrite
		}
		return nil
	})
}

func atomicReplace(target string, operations stateFileOps, write func(io.Writer) error) (returnErr error) {
	directory := filepath.Dir(target)
	temporary, err := operations.createTemp(directory, "."+filepath.Base(target)+".tmp-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	closed := false
	defer func() {
		if !closed {
			returnErr = errors.Join(returnErr, temporary.Close())
		}
		if err := operations.remove(temporaryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			returnErr = errors.Join(returnErr, fmt.Errorf("remove temporary state file: %w", err))
		}
	}()

	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	buffered := bufio.NewWriter(temporary)
	if err := write(buffered); err != nil {
		return err
	}
	if err := buffered.Flush(); err != nil {
		return fmt.Errorf("flush state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		closed = true
		return fmt.Errorf("close state: %w", err)
	}
	closed = true
	if err := operations.rename(temporaryPath, target); err != nil {
		return fmt.Errorf("rename state: %w", err)
	}
	if err := operations.syncDirectory(directory); err != nil {
		return fmt.Errorf("sync state directory %q: %w", directory, err)
	}
	return nil
}

func syncStateDirectory(directory string) error {
	// #nosec G304 -- the state path is an explicit operator input; durability requires opening its directory.
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return err
	}
	return errors.Join(directoryHandle.Sync(), directoryHandle.Close())
}
