// Package network는 mb0 브리지와 veth 쌍으로 컨테이너를 네트워크에 연결한다.
// netlink를 직접 짜는 대신 ip, iptables, nsenter 명령을 호출한다.
//
// 원격(SSH) 장비라서 mb0, 10.88.0.0/24, MINIBOX 체인만 만지고
// 다른 인터페이스와 기본 라우트는 절대 건드리지 않는다.
package network

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	Bridge  = "mb0"
	Gateway = "10.88.0.1"
	Subnet  = "10.88.0.0/24"
	chain   = "MINIBOX"
	ipDir   = "/run/minibox/ips"
)

// Setup은 브리지와 iptables 규칙을 만든다. 이미 있으면 그대로 둔다.
func Setup() error {
	if _, err := os.Stat("/sys/class/net/" + Bridge); err != nil {
		if err := run("ip", "link", "add", Bridge, "type", "bridge"); err != nil {
			return err
		}
		if err := run("ip", "addr", "add", Gateway+"/24", "dev", Bridge); err != nil {
			return err
		}
	}
	if err := run("ip", "link", "set", Bridge, "up"); err != nil {
		return err
	}

	for _, table := range []string{"filter", "nat"} {
		// 체인이 이미 있으면 에러가 나지만 상관없다
		exec.Command("iptables", "-t", table, "-N", chain).Run()
	}
	// 규칙은 전용 체인에만 넣는다. 나중에 체인만 비우면 한 번에 정리된다.
	// Docker가 FORWARD 정책을 DROP으로 바꾸고 br_netfilter를 켜 두어서,
	// 같은 브리지 안의 컨테이너끼리 통신도 FORWARD를 지나간다. 그래서 명시적으로 허용해야 한다.
	for _, r := range [][]string{
		{"filter", "FORWARD", "-j", chain},
		{"filter", chain, "-i", Bridge, "-j", "ACCEPT"},
		{"filter", chain, "-o", Bridge, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
		{"nat", "POSTROUTING", "-j", chain},
		{"nat", chain, "-s", Subnet, "!", "-o", Bridge, "-j", "MASQUERADE"},
	} {
		if err := ensureRule(r[0], r[1], r[2:]...); err != nil {
			return err
		}
	}
	return nil
}

// Alloc은 10.88.0.2~254 중 빈 IP 하나를 잡는다.
// /run/minibox/ips/<IP> 파일을 O_EXCL로 만들어서, 동시에 떠도 같은 IP를 받지 않는다.
// ponytail: minibox가 kill -9로 죽으면 파일이 남는다. 8주차 minibox gc에서 치운다.
func Alloc() (ip string, release func(), err error) {
	if err := os.MkdirAll(ipDir, 0o755); err != nil {
		return "", nil, err
	}
	for i := 2; i < 255; i++ {
		ip := fmt.Sprintf("10.88.0.%d", i)
		path := filepath.Join(ipDir, ip)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		f.Close()
		return ip, func() { os.Remove(path) }, nil
	}
	return "", nil, errors.New("남은 IP 없음")
}

// Attach는 veth 쌍을 만들어 한쪽은 mb0에, 다른 쪽은 pid의 net 네임스페이스에 꽂고
// 그 안에서 eth0으로 이름을 바꿔 IP와 기본 라우트를 설정한다.
// 컨테이너 쪽 veth는 네임스페이스가 사라질 때 커널이 쌍째로 지운다.
func Attach(pid int, ip string) error {
	n := ip[strings.LastIndex(ip, ".")+1:]
	host, peer := "mbv"+n, "mbc"+n
	for _, args := range [][]string{
		{"link", "add", host, "type", "veth", "peer", "name", peer},
		{"link", "set", host, "master", Bridge, "up"},
		{"link", "set", peer, "netns", fmt.Sprint(pid)},
	} {
		if err := run("ip", args...); err != nil {
			return err
		}
	}
	// 호스트의 ip 명령을 컨테이너 net 네임스페이스 안에서만 실행한다
	cmd := exec.Command("nsenter", "-t", fmt.Sprint(pid), "-n", "ip", "-batch", "-")
	cmd.Stdin = strings.NewReader(strings.Join([]string{
		"link set lo up",
		"link set " + peer + " name eth0",
		"addr add " + ip + "/24 dev eth0",
		"link set eth0 up",
		"route add default via " + Gateway,
	}, "\n"))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("컨테이너 네트워크 설정: %v: %s", err, out)
	}
	return nil
}

func ensureRule(table, chainName string, rule ...string) error {
	check := append([]string{"-t", table, "-C", chainName}, rule...)
	if exec.Command("iptables", check...).Run() == nil {
		return nil
	}
	// FORWARD와 POSTROUTING에는 맨 앞에 끼워 넣어 Docker 규칙보다 먼저 보게 한다
	op := "-A"
	if chainName != chain {
		op = "-I"
	}
	return run("iptables", append([]string{"-t", table, op, chainName}, rule...)...)
}

func run(name string, args ...string) error {
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
