//go:build windows

package host

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// conPTY runs a command inside a Windows pseudo console (ConPTY, Windows 10
// 1809+ / Server 2019+), the same mechanism Windows Terminal uses. ConPTY
// speaks VT sequences, so the output streams to xterm.js unchanged.
type conPTY struct {
	hpc       windows.Handle
	proc      windows.Handle
	job       windows.Handle // kill-on-close job: the command tree dies with setu
	in        *os.File       // our end of the console's input pipe
	out       *os.File       // our end of the console's output pipe
	closeOnce sync.Once
	killOnce  sync.Once
}

func coord(cols, rows int) windows.Coord {
	return windows.Coord{X: int16(cols), Y: int16(rows)}
}

func startPTY(command []string, cols, rows int) (ptyProcess, error) {
	if err := windows.NewLazySystemDLL("kernel32.dll").NewProc("CreatePseudoConsole").Find(); err != nil {
		return nil, errors.New("this Windows version has no ConPTY (needs Windows 10 1809 or newer)")
	}
	cmdLine, err := commandLine(command)
	if err != nil {
		return nil, err
	}

	var inR, inW, outR, outW windows.Handle
	if err := windows.CreatePipe(&inR, &inW, nil, 0); err != nil {
		return nil, fmt.Errorf("create input pipe: %w", err)
	}
	if err := windows.CreatePipe(&outR, &outW, nil, 0); err != nil {
		windows.CloseHandle(inR)
		windows.CloseHandle(inW)
		return nil, fmt.Errorf("create output pipe: %w", err)
	}
	var hpc windows.Handle
	if err := windows.CreatePseudoConsole(coord(cols, rows), inR, outW, 0, &hpc); err != nil {
		for _, h := range []windows.Handle{inR, inW, outR, outW} {
			windows.CloseHandle(h)
		}
		return nil, fmt.Errorf("CreatePseudoConsole: %w", err)
	}
	// The pseudo console holds its own duplicates of these ends.
	windows.CloseHandle(inR)
	windows.CloseHandle(outW)

	p := &conPTY{hpc: hpc, in: os.NewFile(uintptr(inW), "conpty-in"), out: os.NewFile(uintptr(outR), "conpty-out")}
	if err := p.spawn(cmdLine); err != nil {
		p.Close()
		return nil, err
	}
	return p, nil
}

func (p *conPTY) spawn(cmdLine string) error {
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return err
	}
	defer attrs.Delete()
	// The attribute value is the HPCON itself, not a pointer to it.
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, *(*unsafe.Pointer)(unsafe.Pointer(&p.hpc)), unsafe.Sizeof(p.hpc)); err != nil {
		return fmt.Errorf("attach pseudo console: %w", err)
	}

	si := &windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	si.Cb = uint32(unsafe.Sizeof(*si))
	// Without this, a child of a process with redirected std handles would
	// write to those handles instead of the pseudo console.
	si.Flags = windows.STARTF_USESTDHANDLES

	cmdLine16, err := windows.UTF16PtrFromString(cmdLine)
	if err != nil {
		return err
	}
	env := append(os.Environ(), "SETU_SESSION=1")
	envBlock, err := environmentBlock(env)
	if err != nil {
		return err
	}
	var pi windows.ProcessInformation
	err = windows.CreateProcess(nil, cmdLine16, nil, nil, false,
		windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT|windows.CREATE_SUSPENDED,
		envBlock, nil, &si.StartupInfo, &pi)
	if err != nil {
		return fmt.Errorf("start %s: %w", cmdLine, err)
	}
	p.proc = pi.Process
	// Put the command in a kill-on-close job before it runs, so it and its
	// children cannot outlive setu (e.g. when a background share is stopped).
	if job, err := killOnCloseJob(); err == nil {
		if windows.AssignProcessToJobObject(job, pi.Process) == nil {
			p.job = job
		} else {
			windows.CloseHandle(job)
		}
	}
	_, _ = windows.ResumeThread(pi.Thread)
	windows.CloseHandle(pi.Thread)
	return nil
}

func killOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

// environmentBlock builds a CREATE_UNICODE_ENVIRONMENT block:
// "k=v\0k=v\0\0" in UTF-16.
func environmentBlock(env []string) (*uint16, error) {
	var b []uint16
	for _, kv := range env {
		if strings.IndexByte(kv, 0) >= 0 {
			return nil, fmt.Errorf("environment variable contains NUL: %q", kv)
		}
		u, err := windows.UTF16FromString(kv) // includes the terminating NUL
		if err != nil {
			return nil, err
		}
		b = append(b, u...)
	}
	b = append(b, 0)
	return &b[0], nil
}

// commandLine resolves the executable on PATH (honouring PATHEXT) and builds
// the CreateProcess command line. Batch shims such as npm's `claude.cmd` run
// through `cmd.exe /d /s /c "<line>"`: with /s cmd strips exactly the outer
// quotes, so the inner, normally quoted line reaches the script intact.
func commandLine(command []string) (string, error) {
	path, err := exec.LookPath(command[0])
	if err != nil {
		return "", err
	}
	line := windows.ComposeCommandLine(append([]string{path}, command[1:]...))
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cmd", ".bat":
		comspec := os.Getenv("COMSPEC")
		if comspec == "" {
			comspec = `C:\Windows\System32\cmd.exe`
		}
		return windows.EscapeArg(comspec) + ` /d /s /c "` + line + `"`, nil
	}
	return line, nil
}

func (p *conPTY) Read(b []byte) (int, error)  { return p.out.Read(b) }
func (p *conPTY) Write(b []byte) (int, error) { return p.in.Write(b) }

func (p *conPTY) Resize(cols, rows int) error {
	return windows.ResizePseudoConsole(p.hpc, coord(cols, rows))
}

func (p *conPTY) Wait() int {
	if p.proc == 0 {
		return 1
	}
	_, _ = windows.WaitForSingleObject(p.proc, windows.INFINITE)
	var code uint32
	if err := windows.GetExitCodeProcess(p.proc, &code); err != nil {
		code = 1
	}
	// Closing the pseudo console flushes the last output and makes Read
	// return EOF; pumpOutput keeps reading meanwhile, so it cannot block.
	p.closeConsole()
	return int(code)
}

func (p *conPTY) Kill() error {
	var err error
	p.killOnce.Do(func() {
		if p.proc != 0 {
			err = windows.TerminateProcess(p.proc, 1)
		}
	})
	return err
}

func (p *conPTY) closeConsole() {
	p.closeOnce.Do(func() {
		windows.ClosePseudoConsole(p.hpc)
		p.in.Close()
	})
}

func (p *conPTY) Close() error {
	p.closeConsole()
	p.out.Close()
	if p.proc != 0 {
		windows.CloseHandle(p.proc)
		p.proc = 0
	}
	if p.job != 0 {
		windows.CloseHandle(p.job) // kills anything the command left behind
		p.job = 0
	}
	return nil
}

// DefaultShell prefers PowerShell 7, then Windows PowerShell, then cmd.exe.
func DefaultShell() []string {
	for _, sh := range []string{"pwsh.exe", "powershell.exe"} {
		if p, err := exec.LookPath(sh); err == nil {
			return []string{p, "-NoLogo"}
		}
	}
	if c := os.Getenv("COMSPEC"); c != "" {
		return []string{c}
	}
	return []string{"cmd.exe"}
}
