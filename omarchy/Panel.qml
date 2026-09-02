import QtQuick
import Quickshell
import Quickshell.Io
import qs.Commons
import qs.Ui

// Moirai in the Omarchy bar.
//
// The bar button shows how many local agent sessions `moirai list --json`
// finds. The popup lists the most recent ones; clicking a row (or Enter on the
// keyboard cursor) runs `moirai continue <id> --from <format> --with <harness>`
// in a terminal through omarchy-launch-tui, so the destination harness opens
// with the session already saved into its native store.
//
// Everything that reaches a process is an argv vector (Util.execArgv, Process
// command arrays); nothing from a session ever becomes a shell string. Session
// ids and format names are re-validated against a strict token pattern before
// they are used, and titles are scrubbed of control characters before display.
Panel {
  id: root
  moduleName: "io.github.october-dev.moirai"
  ipcTarget: "io.github.october-dev.moirai"
  manageIpc: false

  readonly property bool vertical: bar ? bar.vertical : false
  readonly property color foreground: bar ? bar.foreground : Color.foreground
  readonly property color dim: Qt.darker(foreground, 1.55)
  readonly property string fontFamily: bar ? bar.fontFamily : Style.font.family
  readonly property color hoverFill: Style.hoverFillFor(foreground, Color.accent)

  // ---- Settings (inline on this widget's shell.json entry; see manifest.json).
  readonly property int refreshIntervalSec: clampInt(setting("refreshIntervalSec", 120), 10, 3600, 120)
  readonly property int maxSessions: clampInt(setting("maxSessions", 12), 1, 50, 12)
  readonly property string targetHarness: token(setting("continueWith", "claude_code"), "claude_code")
  readonly property string sourceFormat: token(setting("sourceFormat", ""), "")

  // ---- State.
  property var sessions: []
  property string status: "Loading…"
  property string lastError: ""
  property bool cursorActive: false
  property int cursor: -1

  readonly property int count: sessions.length
  readonly property var selected: cursorActive && cursor >= 0 && cursor < sessions.length ? sessions[cursor] : null

  // A session id or format is only ever a plain token. Anything else is
  // dropped rather than passed along.
  function token(value, fallback) {
    var text = String(value === undefined || value === null ? "" : value).trim()
    return /^[A-Za-z0-9._-]{1,128}$/.test(text) ? text : fallback
  }

  function clampInt(value, low, high, fallback) {
    var n = Number(value)
    if (!isFinite(n)) return fallback
    return Math.min(high, Math.max(low, Math.round(n)))
  }

  // Titles and paths come from other people's session files. Control
  // characters never reach the bar, the popup, or a tooltip.
  function scrub(value) {
    return String(value === undefined || value === null ? "" : value).replace(/[\u0000-\u001f\u007f-\u009f]/g, " ").trim()
  }

  function refresh() {
    if (listProcess.running) return
    var argv = ["moirai", "list", "--json"]
    if (root.sourceFormat !== "") argv = argv.concat(["--format", root.sourceFormat])
    listProcess.command = argv
    listProcess.running = true
  }

  function applyListing(text) {
    var parsed = null
    try { parsed = JSON.parse(String(text || "")) } catch (error) { parsed = null }
    if (!parsed || typeof parsed !== "object") {
      root.sessions = []
      root.status = root.lastError !== "" ? root.lastError : "moirai list returned no JSON"
      return
    }
    var found = parsed.sessions instanceof Array ? parsed.sessions : []
    var rows = []
    for (var i = 0; i < found.length && rows.length < root.maxSessions; i++) {
      var entry = found[i] || {}
      var id = token(entry.id, "")
      var format = token(entry.format, "")
      if (id === "" || format === "") continue
      var title = scrub(entry.title)
      var cwd = scrub(entry.cwd)
      rows.push({
        id: id,
        format: format,
        title: title !== "" ? title : (cwd !== "" ? cwd : id),
        cwd: cwd,
        model: scrub(entry.model),
        timestamp: scrub(entry.modified_at || entry.timestamp)
      })
    }
    root.sessions = rows
    root.lastError = ""
    root.status = rows.length === 0 ? "No sessions found" : (rows.length + (rows.length === 1 ? " session" : " sessions"))
    if (root.cursor >= rows.length) root.cursor = rows.length - 1
  }

  function continueSession(row) {
    if (!row) return
    var id = token(row.id, "")
    var from = token(row.format, "")
    var target = root.targetHarness
    if (id === "" || from === "" || target === "") return
    Util.execArgv([
      "omarchy-launch-tui", "--app-id=org.omarchy.moirai",
      "moirai", "continue", id, "--from", from, "--with", target
    ])
    root.close()
  }

  function moveCursor(delta) {
    if (root.sessions.length === 0) return
    root.cursorActive = true
    var next = root.cursor + delta
    if (next < 0) next = root.sessions.length - 1
    if (next >= root.sessions.length) next = 0
    root.cursor = next
  }

  function activateCursor() {
    if (root.selected) root.continueSession(root.selected)
  }

  implicitWidth: button.implicitWidth
  implicitHeight: button.implicitHeight

  Component.onCompleted: root.refresh()

  onOpenedChanged: if (opened) {
    root.cursorActive = false
    if (panelFlick) panelFlick.contentY = 0
    root.refresh()
    Qt.callLater(function() { keyCatcher.forceActiveFocus() })
  }

  Process {
    id: listProcess
    command: ["moirai", "list", "--json"]
    stdout: StdioCollector {
      waitForEnd: true
      onStreamFinished: root.applyListing(text)
    }
    stderr: StdioCollector {
      waitForEnd: true
      onStreamFinished: {
        var lines = String(text || "").split("\n").filter(function(line) { return line.trim() !== "" })
        root.lastError = lines.length > 0 ? root.scrub(lines[lines.length - 1]) : ""
      }
    }
    onExited: function(exitCode, exitStatus) {
      if (exitCode !== 0) {
        root.sessions = []
        root.status = root.lastError !== "" ? root.lastError : ("moirai list failed (exit " + exitCode + "); is moirai on PATH?")
      }
    }
  }

  Timer {
    interval: root.refreshIntervalSec * 1000
    repeat: true
    running: true
    onTriggered: root.refresh()
  }

  IpcHandler {
    target: root.ipcTarget
    function open(): void { root.open() }
    function close(): void { root.close() }
    function show(): void { root.open() }
    function hide(): void { root.close() }
    function toggle(): void { root.toggle() }
    function refresh(): string { root.refresh(); return "ok" }
    function count(): string { return String(root.count) }
    function status(): string { return root.status }
  }

  WidgetButton {
    id: button
    anchors.fill: parent
    bar: root.bar
    text: root.vertical ? String(root.count) : ("moirai" + (root.count > 0 ? " " + root.count : ""))
    tooltipText: "Moirai · " + root.status + " · middle-click to refresh"
    onPressed: function(buttonCode) {
      if (buttonCode === Qt.MiddleButton) root.refresh()
      else root.toggle()
    }
  }

  KeyboardPanel {
    id: panel
    anchorItem: button
    owner: root
    bar: root.bar
    open: root.opened
    focusTarget: keyCatcher
    contentWidth: panel.fittedContentWidth(Style.space(420))
    contentHeight: panel.fittedContentHeight(column.implicitHeight, Style.space(560))

    PanelKeyCatcher {
      id: keyCatcher
      anchors.fill: parent
      onMoveRequested: function(dx, dy) {
        if (dy !== 0) root.moveCursor(dy)
        else if (dx !== 0) root.moveCursor(dx)
      }
      onActivateRequested: root.activateCursor()
      onCloseRequested: root.close()
      onTabRequested: function(direction) { root.switchPanel(direction) }
      onTextKey: function(t) {
        if (t === "r" || t === "R") root.refresh()
        else if (t === "j" || t === "J") root.moveCursor(1)
        else if (t === "k" || t === "K") root.moveCursor(-1)
      }

      Flickable {
        id: panelFlick
        anchors.fill: parent
        contentWidth: width
        contentHeight: column.implicitHeight
        clip: true
        boundsBehavior: Flickable.StopAtBounds
        flickableDirection: Flickable.VerticalFlick
        interactive: contentHeight > height

        Column {
          id: column
          width: panelFlick.width
          spacing: Style.space(10)

          PanelHero {
            width: parent.width
            title: "Moirai"
            meta: root.status
            detail: "Continue with " + root.targetHarness + (root.sourceFormat !== "" ? " · showing " + root.sourceFormat : "")
            foreground: root.foreground
            fontFamily: root.fontFamily
          }

          PanelSeparator {
            foreground: root.foreground
          }

          PanelSectionHeader {
            text: "SESSIONS"
            foreground: root.foreground
            fontFamily: root.fontFamily
          }

          Text {
            visible: root.sessions.length === 0
            width: parent.width
            text: root.status
            color: root.dim
            font.family: root.fontFamily
            font.pixelSize: Style.font.body
            wrapMode: Text.WordWrap
            horizontalAlignment: Text.AlignHCenter
          }

          Column {
            width: parent.width
            spacing: Style.space(4)

            Repeater {
              model: root.sessions

              Rectangle {
                id: row
                required property var modelData
                required property int index
                readonly property bool highlighted: rowMouse.containsMouse || (root.cursorActive && root.cursor === index)

                width: parent.width
                height: rowColumn.implicitHeight + Style.space(12)
                radius: Style.cornerRadius
                color: highlighted ? root.hoverFill : "transparent"

                Column {
                  id: rowColumn
                  anchors.left: parent.left
                  anchors.right: parent.right
                  anchors.verticalCenter: parent.verticalCenter
                  anchors.leftMargin: Style.space(8)
                  anchors.rightMargin: Style.space(8)
                  spacing: Style.space(2)

                  Text {
                    width: parent.width
                    text: row.modelData.title
                    color: root.foreground
                    font.family: root.fontFamily
                    font.pixelSize: Style.font.body
                    elide: Text.ElideRight
                    textFormat: Text.PlainText
                  }

                  Text {
                    width: parent.width
                    text: row.modelData.format + "  ·  " + row.modelData.id.slice(0, 8)
                      + (row.modelData.model !== "" ? "  ·  " + row.modelData.model : "")
                      + (row.modelData.timestamp !== "" ? "  ·  " + row.modelData.timestamp.slice(0, 16).replace("T", " ") : "")
                    color: root.dim
                    font.family: root.fontFamily
                    font.pixelSize: Style.font.caption
                    elide: Text.ElideRight
                    textFormat: Text.PlainText
                  }
                }

                MouseArea {
                  id: rowMouse
                  anchors.fill: parent
                  hoverEnabled: true
                  cursorShape: Qt.PointingHandCursor
                  onEntered: { root.cursorActive = true; root.cursor = row.index }
                  onClicked: root.continueSession(row.modelData)
                }
              }
            }
          }

          Text {
            width: parent.width
            text: "Enter or click continues a session · r refreshes · Esc closes"
            color: root.dim
            font.family: root.fontFamily
            font.pixelSize: Style.font.caption
            horizontalAlignment: Text.AlignHCenter
            wrapMode: Text.WordWrap
          }
        }
      }
    }
  }
}
