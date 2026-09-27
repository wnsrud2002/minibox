// Package container는 새 네임스페이스 안에서 명령을 실행한다.
package container

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// Hostname은 컨테이너 안에서 보이는 호스트네임이다.
const Hostname = "minibox"

// Run은 자기 자신(/proc/self/exe)을 "init" 서브커맨드로 다시 실행한다.
// 새 네임스페이스는 clone 시점에만 만들 수 있으므로, 부모는 네임스페이스를
// 만들어 자식을 띄우는 일만 하고 실제 설정은 자식(Init)이 안에서 한다.
func Run(args []string) (int, error) {
	cmd := exec.Command("/proc/self/exe", append([]string{"init"}, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUTS | syscall.CLONE_NEWPID,
		// minibox가 죽으면 컨테이너도 같이 죽게 한다
		Pdeathsig: syscall.SIGKILL,
	}
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), nil
		}
		return 1, err
	}
	return 0, nil
}

// Init은 새 네임스페이스 안에서 PID 1로 실행된다.
// 호스트네임을 바꾼 뒤 사용자 명령으로 자기 자신을 교체(exec)하므로
// 사용자 명령이 그대로 PID 1이 된다.
func Init(args []string) error {
	if err := syscall.Sethostname([]byte(Hostname)); err != nil {
		return fmt.Errorf("sethostname: %w", err)
	}
	path, err := exec.LookPath(args[0])
	if err != nil {
		return err
	}
	return syscall.Exec(path, args, os.Environ())
}
