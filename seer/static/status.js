"use strict";

const blocks = new Map();
let logs = [""];

async function fetchJSON(path) {
    const response = await fetch(path, {
        method: "GET",
        cache: "no-cache",
        credentials: "same-origin",
        redirect: "error",
        referrerPolicy: "no-referrer"
    });
    if (!response.ok) {
        throw new Error(`${path}: HTTP ${response.status}`);
    }
    return response.json();
}

async function loadState() {
    try {
        const logSetting = await fetchJSON("logsenabled");
        if (logSetting.enabled === false) {
            document.getElementById("logContainer").hidden = true;
        }
        const initialState = await fetchJSON("state");
        updateTable(initialState);
        drawSeries(initialState);
        if (logSetting.enabled !== false) {
            const initialLogs = await fetchJSON("logs");
            for (let i = initialLogs.length - 1; i >= 0; i--) {
                const message = initialLogs[i].ts === 0 ? "" : `${new Date(initialLogs[i].ts * 1000).toLocaleTimeString()} - ${initialLogs[i].msg}`;
                addLogMsg(message);
            }
        }
    } catch (error) {
        addLogMsg(`Dashboard state unavailable: ${error.message}`);
    }
}

function textCell(row, value, className = "") {
    const cell = row.insertCell();
    cell.textContent = value;
    if (className) {
        cell.className = className;
    }
    return cell;
}

function updateTable(status) {
    if (!status || !Array.isArray(status.Status)) {
        return;
    }
    const table = document.getElementById("statusTable");
    table.replaceChildren();

    status.Status.forEach((item) => {
        const row = table.insertRow();
        const alertCell = row.insertCell();
        if (item.active_alerts > 0 || item.last_error) {
            const issue = document.createElement("details");
            issue.className = "issue";
            const summary = document.createElement("summary");
            summary.textContent = `⚠ ${item.active_alerts || 1}`;
            summary.title = `${item.active_alerts || 1} active issue(s)`;
            issue.appendChild(summary);
            if (item.last_error) {
                const detail = document.createElement("pre");
                detail.textContent = item.last_error;
                issue.appendChild(detail);
            }
            alertCell.appendChild(issue);
        }

        textCell(row, `${item.name} (${item.chain_id})`);
        const height = textCell(row, String(item.height), "mono");
        if (blocks.get(item.chain_id) !== item.height) {
            height.classList.add("pulse");
        }
        blocks.set(item.chain_id, item.height);

        const moniker = textCell(row, item.moniker === "not connected" ? "not connected" : String(item.moniker).slice(0, 24));
        if (item.moniker === "not connected") {
            moniker.className = "status-warning";
        }

        let bonded = "○ Not active";
        let bondedClass = "status-warning";
        if (item.tombstoned) {
            bonded = "☠ Tombstoned";
            bondedClass = "status-danger";
        } else if (item.jailed) {
            bonded = "⚠ Jailed";
            bondedClass = "status-danger";
        } else if (item.bonded) {
            bonded = "✓ Bonded";
            bondedClass = "status-good";
        } else if (item.moniker === "not connected") {
            bonded = "Unknown";
        }
        textCell(row, bonded, bondedClass);

        let uptime = "error";
        if (item.window > 0) {
            uptime = `${(100 - (item.missed / item.window) * 100).toFixed(2)}% · ${item.missed} / ${item.window}`;
        }
        textCell(row, uptime, "uptime");

        const nodeClass = item.healthy_nodes < item.nodes ? "status-warning" : "status-good";
        textCell(row, `${item.healthy_nodes} / ${item.nodes}`, nodeClass);
    });
}

function addLogMsg(message) {
    if (logs.length >= 256) {
        logs.pop();
    }
    logs.unshift(message);
    if (document.visibilityState !== "hidden") {
        document.getElementById("logs").textContent = logs.join("\n");
    }
}

function setConnection(state, label) {
    const badge = document.getElementById("connectionState");
    badge.className = `connection ${state}`;
    badge.textContent = label;
}

function connect() {
    const protocol = location.protocol === "https:" ? "wss://" : "ws://";
    const socket = new WebSocket(protocol + location.host + "/ws");
    socket.addEventListener("open", () => setConnection("live", "watching"));
    socket.addEventListener("message", (event) => {
        const message = JSON.parse(event.data);
        if (message.msgType === "log") {
            addLogMsg(`${new Date(message.ts * 1000).toLocaleTimeString()} - ${message.msg}`);
        } else if (message.msgType === "update" && document.visibilityState !== "hidden") {
            updateTable(message);
            drawSeries(message);
        }
    });
    socket.addEventListener("close", (event) => {
        setConnection("lost", "reconnecting");
        addLogMsg(`Watch connection closed; retrying (${event.reason || "no reason"})`);
        window.setTimeout(connect, 3000);
    });
    socket.addEventListener("error", () => socket.close());
}

document.addEventListener("DOMContentLoaded", () => {
    document.getElementById("themeToggle").addEventListener("click", lightMode);
    legend();
    loadState();
    connect();
});
