// Package container는 새 네임스페이스 안에서 명령을 실행한다.
package container

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// Hostname은 컨테이너 안에서 보이는 호스트네임이다.
const Hostname = "minibox"

// Run은 자기 자신(/proc/self/exe)을 "init" 서브커맨드로 다시 실행한다.
// 새 네임스페이스는 clone 시점에만 만들 수 있으므로, 부모는 네임스페이스를
// 만들어 자식을 띄우는 일만 하고 실제 설정은 자식(Init)이 안에서 한다.
func Run(image string, args []string) (int, error) {
	lower, err := filepath.Abs(filepath.Join("images", image))
	if err != nil {
		return 1, err
	}
	if _, err := os.Stat(filepath.Join(lower, "bin")); err != nil {
		return 1, fmt.Errorf("이미지 없음: %s", lower)
	}
	// 컨테이너별 쓰기 레이어. 컨테이너가 끝나면 통째로 지운다.
	dir, err := os.MkdirTemp("", "minibox-")
	if err != nil {
		return 1, err
	}
	defer os.RemoveAll(dir)

	cmd := exec.Command("/proc/self/exe", append([]string{"init", lower, dir}, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUTS | syscall.CLONE_NEWPID | syscall.CLONE_NEWNS,
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
// 루트 파일시스템을 이미지로 바꾸고, 사용자 명령으로 자기 자신을
// 교체(exec)하므로 사용자 명령이 그대로 PID 1이 된다.
func Init(lower, dir string, args []string) error {
	if err := syscall.Sethostname([]byte(Hostname)); err != nil {
		return fmt.Errorf("sethostname: %w", err)
	}
	if err := setupRoot(lower, dir); err != nil {
		return err
	}
	path, err := exec.LookPath(args[0])
	if err != nil {
		return err
	}
	return syscall.Exec(path, args, os.Environ())
}

// setupRoot는 overlayfs로 이미지 위에 쓰기 레이어를 얹고, 그곳으로 pivot_root한다.
// 모든 마운트는 새 mount 네임스페이스 안에서만 보이므로, 컨테이너가 끝나면 커널이 알아서 걷어낸다.
func setupRoot(lower, dir string) error {
	// 호스트의 / 는 보통 shared라서, 그대로 두면 여기서 한 마운트가 호스트로 새어 나간다
	if err := syscall.Mount("", "/", "", syscall.MS_PRIVATE|syscall.MS_REC, ""); err != nil {
		return fmt.Errorf("make / private: %w", err)
	}

	upper, work, merged := filepath.Join(dir, "upper"), filepath.Join(dir, "work"), filepath.Join(dir, "merged")
	for _, d := range []string{upper, work, merged} {
		if err := os.Mkdir(d, 0o755); err != nil {
			return err
		}
	}
	// lower(이미지)는 읽기 전용으로 공유되고, 변경은 upper에만 쌓인다
	opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", lower, upper, work)
	if err := syscall.Mount("overlay", merged, "overlay", 0, opts); err != nil {
		return fmt.Errorf("mount overlay: %w", err)
	}

	// pivot_root: merged를 새 / 로, 기존 / 는 /.oldroot로 옮긴 뒤 떼어 낸다
	old := filepath.Join(merged, ".oldroot")
	if err := os.Mkdir(old, 0o700); err != nil {
		return err
	}
	if err := syscall.PivotRoot(merged, old); err != nil {
		return fmt.Errorf("pivot_root: %w", err)
	}
	if err := os.Chdir("/"); err != nil {
		return err
	}
	if err := syscall.Unmount("/.oldroot", syscall.MNT_DETACH); err != nil {
		return fmt.Errorf("unmount oldroot: %w", err)
	}
	if err := os.Remove("/.oldroot"); err != nil {
		return err
	}

	// /proc을 새로 마운트해야 ps가 이 PID 네임스페이스의 프로세스만 보여 준다
	if err := syscall.Mount("proc", "/proc", "proc", 0, ""); err != nil {
		return fmt.Errorf("mount /proc: %w", err)
	}
	return nil
}
