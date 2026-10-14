package service

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

const taskName = `setu`

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// renderTask builds a Task Scheduler definition that starts the supervisor
// at logon, in the user's session, with no time limit (the default kills
// tasks after 72 hours) and least privilege.
func renderTask(u Unit, userID string) string {
	args := append(u.Args(), "--no-console")
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>setu background terminal shares</Description>
    <URI>\` + taskName + `</URI>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>` + xmlEscape(userID) + `</UserId>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>` + xmlEscape(userID) + `</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>7</Priority>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>999</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + xmlEscape(u.Exe) + `</Command>
      <Arguments>` + xmlEscape(windows.ComposeCommandLine(args)) + `</Arguments>
    </Exec>
  </Actions>
</Task>
`
}

// utf16File encodes s as UTF-16LE with a byte order mark, as schtasks expects.
func utf16File(s string) []byte {
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xFE})
	for _, c := range utf16.Encode([]rune(s)) {
		_ = binary.Write(&b, binary.LittleEndian, c)
	}
	return b.Bytes()
}

// Install registers and starts a logon task for the current user.
func Install(u Unit, start bool) (string, error) {
	cur, err := user.Current()
	if err != nil {
		return "", err
	}
	xmlPath := filepath.Join(u.Dir, "task.xml")
	if err := writeFile(xmlPath, utf16File(renderTask(u, cur.Username)), 0o600); err != nil {
		return "", err
	}
	defer os.Remove(xmlPath)
	if err := run("schtasks", "/Create", "/TN", taskName, "/XML", xmlPath, "/F"); err != nil {
		return "", err
	}
	if start {
		if err := run("schtasks", "/Run", "/TN", taskName); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf(`installed scheduled task %q (starts at logon, runs hidden)
  logs:   %s`, taskName, u.LogFile), nil
}

// Uninstall stops and deletes the task. Ending the task ends the supervisor
// and, through their kill-on-close job objects, the shared commands.
func Uninstall() error {
	_ = run("schtasks", "/End", "/TN", taskName)
	return run("schtasks", "/Delete", "/TN", taskName, "/F")
}

// AutostartStatus describes the autostart registration.
func AutostartStatus() string {
	out, err := exec.Command("schtasks", "/Query", "/TN", taskName, "/FO", "LIST").Output()
	if err != nil {
		return "not installed (run `setu service install`)"
	}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "Status" {
			return fmt.Sprintf("scheduled task %q: %s", taskName, strings.TrimSpace(v))
		}
	}
	return fmt.Sprintf("scheduled task %q: installed", taskName)
}
