// Package container는 새 네임스페이스 안에서 명령을 실행한다.
package container

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"minibox/internal/cgroup"
	"minibox/internal/network"
)

// Hostname은 컨테이너 안에서 보이는 호스트네임이다.
const Hostname = "minibox"

// Run은 자기 자신(/proc/self/exe)을 "init" 서브커맨드로 다시 실행한다.
// 새 네임스페이스는 clone 시점에만 만들 수 있으므로, 부모는 네임스페이스를
// 만들어 자식을 띄우는 일만 하고 실제 설정은 자식(Init)이 안에서 한다.
func Run(image string, limits cgroup.Limits, args []string) (int, error) {
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

	cg, err := cgroup.Create(filepath.Base(dir), limits)
	if err != nil {
		return 1, fmt.Errorf("cgroup: %w", err)
	}
	defer cgroup.Remove(cg)
	cgFD, err := syscall.Open(cg, syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		return 1, err
	}
	defer syscall.Close(cgFD)

	if err := network.Setup(); err != nil {
		return 1, fmt.Errorf("network: %w", err)
	}
	ip, release, err := network.Alloc()
	if err != nil {
		return 1, err
	}
	defer release()
	// 네트워크는 부모가 컨테이너 밖에서 설정한다. 그동안 자식은 이 파이프에서
	// 기다리다가, 부모가 쓰기 쪽을 닫으면(EOF) 그때 사용자 명령을 실행한다.
	syncR, syncW, err := os.Pipe()
	if err != nil {
		return 1, err
	}
	defer syncW.Close()

	cmd := exec.Command("/proc/self/exe", append([]string{"init", lower, dir}, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.ExtraFiles = []*os.File{syncR} // 자식에게는 fd 3
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUTS | syscall.CLONE_NEWPID | syscall.CLONE_NEWNS | syscall.CLONE_NEWNET,
		// minibox가 죽으면 컨테이너도 같이 죽게 한다
		Pdeathsig: syscall.SIGKILL,
		// clone 시점에 바로 cgroup 안에서 태어나게 한다(CLONE_INTO_CGROUP).
		// 띄운 뒤에 cgroup.procs에 PID를 쓰면 그 사이에 제한 없이 fork할 틈이 생긴다.
		UseCgroupFD: true,
		CgroupFD:    cgFD,
	}
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return 1, err
	}
	syncR.Close()
	if err := network.Attach(cmd.Process.Pid, ip); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return 1, fmt.Errorf("network: %w", err)
	}
	fmt.Fprintln(os.Stderr, "[minibox] IP", ip)
	syncW.Close()
	// 컨테이너의 PID 1은 핸들러 없는 시그널을 커널이 버려서 Ctrl+C로 안 죽을 수 있다.
	// minibox가 대신 받아서 SIGKILL로 끝내야 아래 정리(cgroup, 임시 디렉터리)까지 간다.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)
	go func() {
		for range sigs {
			cmd.Process.Kill()
		}
	}()
	err = cmd.Wait()
	fmt.Fprintln(os.Stderr, "[minibox]", cgroup.Summary(cg, time.Since(start)))
	if err != nil {
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
	// 부모가 네트워크 설정을 끝낼 때까지 기다린다
	sync := os.NewFile(3, "sync")
	io.Copy(io.Discard, sync)
	sync.Close()

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
	if err := setupDev(); err != nil {
		return err
	}
	// 호스트의 resolv.conf는 127.0.0.53(systemd-resolved)이라 컨테이너에서 닿지 않는다
	return os.WriteFile("/etc/resolv.conf", []byte("nameserver 8.8.8.8\n"), 0o644)
}

// setupDev는 /dev를 tmpfs로 새로 만들고 기본 장치 파일만 넣는다.
// 호스트 /dev를 통째로 보여 주면 디스크 같은 장치까지 컨테이너에서 건드릴 수 있다.
func setupDev() error {
	if err := syscall.Mount("tmpfs", "/dev", "tmpfs", syscall.MS_NOSUID, "mode=755"); err != nil {
		return fmt.Errorf("mount /dev: %w", err)
	}
	for name, dev := range map[string][2]int{
		"null": {1, 3}, "zero": {1, 5}, "full": {1, 7},
		"random": {1, 8}, "urandom": {1, 9}, "tty": {5, 0},
	} {
		path := "/dev/" + name
		if err := syscall.Mknod(path, syscall.S_IFCHR|0o666, dev[0]<<8|dev[1]); err != nil {
			return fmt.Errorf("mknod %s: %w", path, err)
		}
		// mknod의 권한은 umask에 깎이므로 다시 맞춘다
		if err := os.Chmod(path, 0o666); err != nil {
			return err
		}
	}
	return nil
}
