#!/bin/sh
# 데모 녹화용: 격리, 자원 제한, 컨테이너 네트워크를 30초 안에 차례로 보여 준다.
# 사용: sudo -v && clear && sudo ./scripts/demo.sh
set -u
cd "$(dirname "$0")/.."

say() { printf '\n\033[1;36m# %s\033[0m\n' "$1"; sleep 1; }
# 화면에 보이는 명령과 실제로 도는 명령이 같도록 문자열 하나로 받는다
run() { printf '\033[1m$ %s\033[0m\n' "$1"; sleep 0.7; sh -c "$1"; sleep 2; }

say "호스트"
run 'hostname'

say "컨테이너 안: PID 1, 다른 호스트네임, 자기 프로세스만 보임"
run "./minibox run alpine sh -c 'echo PID=\$\$; hostname; ps; true'"

say "fork bomb → pids.max=20에서 막힘"
run "./minibox run --pids 20 alpine sh -c 'exec 2>/dev/null; f(){ f|f & }; f; sleep 2'"

say "메모리 64MiB 초과 → OOM kill"
run './minibox run --mem 64m alpine tail /dev/zero'

say "컨테이너 A(10.88.0.2, 웹서버) ← B(wget), 브리지 mb0 위에서 통신"
./minibox run alpine sh -c "printf 'HTTP/1.0 200 OK\r\nContent-Length: 6\r\n\r\nhello\n' | nc -l -p 80 >/dev/null" >/dev/null 2>&1 &
sleep 1
run './minibox run alpine wget -qO- http://10.88.0.2/'
wait
