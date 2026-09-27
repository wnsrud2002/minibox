package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"minibox/internal/cgroup"
	"minibox/internal/container"
	"minibox/internal/dashboard"
	"minibox/internal/network"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: minibox check | ps | stop <id|ip> | gc [--net] | serve [--addr :7070] | net chaos|clear | run [--mem 64m] [--cpu 0.5] [--pids 64] <image> <cmd> [args...]")
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
	case "ps":
		ps()
	case "stop":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: minibox stop <id|ip>")
			os.Exit(2)
		}
		if err := stop(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "stop:", err)
			os.Exit(1)
		}
	case "gc":
		gc(len(os.Args) > 2 && os.Args[2] == "--net")
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

// ps는 실행 중인 컨테이너를 보여 준다. 컨테이너 목록은 cgroup 디렉터리 자체다.
func ps() {
	ips := network.IPs()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tPID\tIP\tCMD")
	for _, dir := range cgroup.List() {
		id := filepath.Base(dir)
		pid := cgroup.InitPID(dir)
		cmd, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\n", id, pid, ips[id], strings.TrimSpace(strings.ReplaceAll(string(cmd), "\x00", " ")))
	}
	w.Flush()
}

// stop은 ID, minibox- 뒤의 숫자, IP 중 하나로 컨테이너를 찾아 끝낸다.
func stop(id string) error {
	ips := network.IPs()
	for _, dir := range cgroup.List() {
		if base := filepath.Base(dir); base == id || base == "minibox-"+id || ips[base] == id {
			return cgroup.Kill(dir)
		}
	}
	return fmt.Errorf("컨테이너 없음: %s", id)
}

// gc는 minibox가 kill -9 등으로 정리 없이 죽었을 때 남는 찌꺼기를 치운다.
// 마운트와 veth는 네임스페이스와 함께 커널이 없애므로 여기서 할 일이 없다.
// net이면 컨테이너가 하나도 없을 때 mb0 브리지와 iptables 체인까지 지운다.
func gc(net bool) {
	live := map[string]bool{}
	for _, dir := range cgroup.List() {
		// 컨테이너를 띄운 minibox가 kill -9로 죽으면 Pdeathsig가 오지 않아 컨테이너가 고아로 남는 경우가 있다.
		// 부모가 더 이상 minibox가 아니면 cgroup째로 끝낸다.
		if pid := cgroup.InitPID(dir); pid > 0 && !parentIsMinibox(pid) {
			cgroup.Kill(dir)
			fmt.Println("고아 컨테이너 종료:", filepath.Base(dir))
			for i := 0; i < 50 && !cgroup.Empty(dir); i++ {
				time.Sleep(20 * time.Millisecond)
			}
		}
		if cgroup.Empty(dir) && os.Remove(dir) == nil {
			fmt.Println("cgroup 삭제:", dir)
			continue
		}
		live[filepath.Base(dir)] = true
	}
	dirs, _ := filepath.Glob(filepath.Join(os.TempDir(), "minibox-*"))
	for _, d := range dirs {
		if !live[filepath.Base(d)] && os.RemoveAll(d) == nil {
			fmt.Println("임시 디렉터리 삭제:", d)
		}
	}
	for _, ip := range network.ReleaseStale(live) {
		fmt.Println("IP 반납:", ip)
	}
	if net {
		if len(live) > 0 {
			fmt.Printf("실행 중인 컨테이너 %d개가 있어서 네트워크는 남겨 둠\n", len(live))
			return
		}
		network.Teardown()
		fmt.Println("mb0 브리지와 MINIBOX 체인 삭제")
	}
}

// parentIsMinibox는 pid의 부모 프로세스가 minibox인지 본다. /proc/<pid>/stat의 4번째 값이 부모 PID다.
func parentIsMinibox(pid int) bool {
	stat, _ := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	// 2번째 값(명령 이름)에 공백이 있을 수 있어서 마지막 ')' 뒤부터 센다
	f := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
	if len(f) < 2 {
		return false
	}
	comm, _ := os.ReadFile("/proc/" + f[1] + "/comm")
	return strings.TrimSpace(string(comm)) == "minibox"
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
