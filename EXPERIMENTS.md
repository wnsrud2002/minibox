# 실험 기록

환경: Jetson Orin Nano (6코어, Ubuntu 24.04, 커널 6.8 tegra), cgroup v2, alpine 3.24.2

## 3주차: cgroup 자원 제한 (2026-09-27)

| 실험 | 명령 | 결과 |
|---|---|---|
| fork bomb | `run --pids 20 alpine /bin/sh -c 'f(){ f\|f & }; f; sleep 3'` | 프로세스 최대 **20**, 막힌 fork 11회, 호스트 영향 없음 |
| 메모리 폭주 | `run --mem 64m alpine tail /dev/zero` | 메모리 최대 **64.0MiB**, oom_kill **1**, 0.2초 만에 종료 |
| CPU 무한 루프 | `run --cpu 0.5 alpine sh -c '... while :; do :; done'` | 60.7초 동안 평균 **0.50코어** (설정값과 일치) |

수치는 종료 시 cgroup 파일에서 읽는다: `cpu.stat`(usage_usec), `memory.peak`, `memory.events`(oom_kill), `pids.peak`, `pids.events`(max).

## 트러블슈팅

- **fork bomb이 `/dev/null` 없음으로 실패:** `&`로 띄운 작업은 입력을 `/dev/null`로 돌린다. rootfs의 `/dev`가 비어 있어서 폭탄이 fork 제한에 닿기 전에 죽었다. → `/dev`에 tmpfs를 올리고 null, zero 등 장치 6개만 mknod했다.
- **pids 제한이 스레드도 센다:** `tail` 하나인데 `pids.peak`가 5였다. exec 전의 minibox(Go 런타임)가 만든 스레드가 잡혔다.
- **PID 1이 SIGTERM과 Ctrl+C를 무시:** busybox `timeout`과 `sh -c '명령 하나'`는 둘 다 자기 자신을 명령으로 exec한다. 그래서 무한 루프가 PID 1이 됐다. 커널은 핸들러 없는 시그널을 PID 1에게 전달하지 않으므로 끝나지 않았다(도커 `--init`이 있는 이유). → minibox가 SIGINT와 SIGTERM을 받아 컨테이너를 SIGKILL하고 cgroup과 임시 디렉터리를 정리하게 했다.
- **swap이 있으면 OOM 대신 느려진다:** Orin Nano에는 8G swapfile이 있다. `memory.swap.max=0`을 함께 걸었다.
