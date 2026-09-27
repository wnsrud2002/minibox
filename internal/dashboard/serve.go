// Package dashboard는 컨테이너 상태를 1초마다 SSE로 브라우저에 흘려보낸다.
package dashboard

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"minibox/internal/cgroup"
	"minibox/web"
)

//go:embed index.html
var page []byte

// 비교할 네임스페이스. minibox가 분리하는 것과 일부러 분리하지 않는 것(user, cgroup, time)을 같이 보여 준다.
var namespaces = []string{"pid", "uts", "mnt", "net", "ipc", "user", "cgroup", "time"}

type container struct {
	ID       string          `json:"id"`
	PID      int             `json:"pid"` // 호스트에서 본 컨테이너 PID 1
	Cmd      string          `json:"cmd"`
	Isolated map[string]bool `json:"isolated"`
	cgroup.Usage
}

func Serve(addr string) error {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(page)
	})
	http.HandleFunc("/events", events)
	http.Handle("/travel/", http.StripPrefix("/travel/", http.FileServer(http.FS(web.FS))))
	// tcpdump로 captures/에 저장한 파일을 여행기에서 ?src=/captures/x.pcap으로 연다
	http.Handle("/captures/", http.StripPrefix("/captures/", http.FileServer(http.Dir("captures"))))
	fmt.Printf("대시보드: http://%s\n", addr)
	return http.ListenAndServe(addr, nil)
}

// events는 연결이 끊길 때까지 1초마다 컨테이너 목록을 보낸다.
func events(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		b, _ := json.Marshal(snapshot())
		fmt.Fprintf(w, "data: %s\n\n", b)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
		}
	}
}

func snapshot() []container {
	list := []container{}
	for _, dir := range cgroup.List() {
		c := container{ID: filepath.Base(dir), Usage: cgroup.Read(dir), Isolated: map[string]bool{}}
		c.PID = initPID(dir)
		if c.PID > 0 {
			cmd, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", c.PID))
			c.Cmd = strings.TrimSpace(strings.ReplaceAll(string(cmd), "\x00", " "))
			for _, ns := range namespaces {
				// 네임스페이스 링크 값("pid:[4026531836]")의 inode가 호스트와 다르면 분리된 것이다
				mine, err1 := os.Readlink(fmt.Sprintf("/proc/%d/ns/%s", c.PID, ns))
				host, err2 := os.Readlink("/proc/self/ns/" + ns)
				if err1 == nil && err2 == nil {
					c.Isolated[ns] = mine != host
				}
			}
		}
		list = append(list, c)
	}
	return list
}

// initPID는 cgroup 안에서 컨테이너 PID 네임스페이스의 1번 프로세스를 찾는다.
// /proc/<pid>/status의 NSpid 줄 마지막 값이 안쪽 네임스페이스에서 본 PID다.
func initPID(dir string) int {
	b, _ := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
	for _, p := range strings.Fields(string(b)) {
		status, _ := os.ReadFile("/proc/" + p + "/status")
		for _, line := range strings.Split(string(status), "\n") {
			if f := strings.Fields(line); len(f) > 2 && f[0] == "NSpid:" && f[len(f)-1] == "1" {
				pid, _ := strconv.Atoi(p)
				return pid
			}
		}
	}
	return 0
}
