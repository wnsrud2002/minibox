package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"minibox/internal/container"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: minibox check | run <cmd> [args...]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "check":
		if !check() {
			os.Exit(1)
		}
	case "run":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: minibox run <cmd> [args...]")
			os.Exit(2)
		}
		code, err := container.Run(os.Args[2:])
		if err != nil {
			fmt.Fprintln(os.Stderr, "run:", err)
		}
		os.Exit(code)
	case "init":
		// run이 내부적으로 부르는 서브커맨드. 새 네임스페이스 안에서 실행된다.
		if err := container.Init(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "init:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(2)
	}
}

// check는 1~7주차에 필요한 호스트 조건을 미리 확인한다.
// 막혔을 때 코드 문제인지 환경 문제인지 바로 가르기 위해서다.
func check() bool {
	ok := true
	report := func(pass bool, name, hint string) {
		if pass {
			fmt.Println("✅", name)
			return
		}
		ok = false
		fmt.Printf("❌ %s  → %s\n", name, hint)
	}

	_, err := os.Stat("/sys/fs/cgroup/cgroup.controllers")
	report(err == nil, "cgroup v2", "cgroup v2가 마운트되어 있지 않음")

	ctrl, _ := os.ReadFile("/sys/fs/cgroup/cgroup.subtree_control")
	for _, c := range []string{"cpu", "memory", "pids"} {
		report(hasField(string(ctrl), c), "cgroup 컨트롤러 "+c, "/sys/fs/cgroup/cgroup.subtree_control에 "+c+" 없음")
	}

	_, err = os.Stat("images/alpine/bin/busybox")
	report(err == nil, "alpine rootfs", "./scripts/fetch-alpine.sh 실행")

	for _, cmd := range []string{"ip", "iptables", "tc", "tcpdump"} {
		_, err := exec.LookPath(cmd)
		report(err == nil, "명령어 "+cmd, "sudo apt install 필요")
	}
	return ok
}

func hasField(s, want string) bool {
	for _, f := range strings.Fields(s) {
		if f == want {
			return true
		}
	}
	return false
}
