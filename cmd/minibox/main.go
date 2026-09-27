package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"minibox/internal/cgroup"
	"minibox/internal/container"
	"minibox/internal/dashboard"
	"minibox/internal/network"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: minibox check | serve [--addr :7070] | net chaos|clear | run [--mem 64m] [--cpu 0.5] [--pids 64] <image> <cmd> [args...]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "check":
		if !check() {
			os.Exit(1)
		}
	case "run":
		fs := flag.NewFlagSet("run", flag.ExitOnError)
		mem := fs.String("mem", "", "메모리 제한 (예: 64m)")
		cpu := fs.Float64("cpu", 0, "CPU 제한, 코어 수 (예: 0.5)")
		pids := fs.Int("pids", 0, "프로세스 수 제한")
		fs.Parse(os.Args[2:])
		if fs.NArg() < 2 {
			fmt.Fprintln(os.Stderr, "usage: minibox run [--mem 64m] [--cpu 0.5] [--pids 64] <image> <cmd> [args...]")
			os.Exit(2)
		}
		limits := cgroup.Limits{CPU: *cpu, Pids: *pids}
		if *mem != "" {
			var err error
			if limits.Mem, err = cgroup.ParseSize(*mem); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}
		}
		code, err := container.Run(fs.Arg(0), limits, fs.Args()[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, "run:", err)
		}
		os.Exit(code)
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		addr := fs.String("addr", "0.0.0.0:7070", "대시보드 주소")
		fs.Parse(os.Args[2:])
		if err := dashboard.Serve(*addr); err != nil {
			fmt.Fprintln(os.Stderr, "serve:", err)
			os.Exit(1)
		}
	case "net":
		if err := netCmd(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "net:", err)
			os.Exit(1)
		}
	case "init":
		// run이 내부적으로 부르는 서브커맨드. 새 네임스페이스 안에서 실행된다.
		if err := container.Init(os.Args[2], os.Args[3], os.Args[4:]); err != nil {
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

// netCmd: minibox net chaos [--loss 10%] [--rate 1mbit] | minibox net clear
func netCmd(args []string) error {
	if len(args) > 0 && args[0] == "clear" {
		if err := network.ClearChaos(); err != nil {
			return err
		}
		fmt.Println("장애 주입 해제")
		return nil
	}
	if len(args) == 0 || args[0] != "chaos" {
		return fmt.Errorf("usage: minibox net chaos [--loss 10%%] [--rate 1mbit] | minibox net clear")
	}
	fs := flag.NewFlagSet("chaos", flag.ExitOnError)
	lossStr := fs.String("loss", "0", "패킷 손실률 (예: 10%)")
	rate := fs.String("rate", "", "컨테이너로 들어가는 대역폭 제한 (예: 1mbit)")
	fs.Parse(args[1:])
	loss, err := strconv.ParseFloat(strings.TrimSuffix(*lossStr, "%"), 64)
	if err != nil || loss < 0 || loss > 100 {
		return fmt.Errorf("잘못된 손실률: %q", *lossStr)
	}
	if err := network.Chaos(loss/100, *rate); err != nil {
		return err
	}
	fmt.Printf("장애 주입: 손실 %g%%, 대역폭 %s\n", loss, map[bool]string{true: "제한 없음", false: *rate}[*rate == ""])
	return nil
}

func hasField(s, want string) bool {
	for _, f := range strings.Fields(s) {
		if f == want {
			return true
		}
	}
	return false
}
