// node --test web/ 으로 실행한다.
import { test } from "node:test";
import assert from "node:assert/strict";
import { parsePcap, groupFlows, FLAG } from "./pcap.js";

// 세그먼트 목록 [출발, 도착, 플래그, seq, ack, 길이]로 가짜 pcap을 만든다.
const C = [10, 88, 0, 3], S = [10, 88, 0, 2];
function fakePcap(littleEndian, segs = HTTP) {
  const cport = 40000, sport = 80;
  return build(littleEndian, segs, cport, sport);
}
// 3-way 핸드셰이크 → 요청/응답 → 종료
const HTTP = [
    [C, S, FLAG.SYN, 1000, 0, 0],
    [S, C, FLAG.SYN | FLAG.ACK, 5000, 1001, 0],
    [C, S, FLAG.ACK, 1001, 5001, 0],
    [C, S, FLAG.PSH | FLAG.ACK, 1001, 5001, 20],
    [S, C, FLAG.PSH | FLAG.ACK, 5001, 1021, 50],
    [C, S, FLAG.FIN | FLAG.ACK, 1021, 5051, 0],
    [S, C, FLAG.FIN | FLAG.ACK, 5051, 1022, 0],
    [C, S, FLAG.ACK, 1022, 5052, 0],
];
function build(littleEndian, segs, cport, sport) {
  const frames = segs.map((seg, i) => {
    const [src, dst, flags, seq, ack, len, t = i * 0.001] = seg;
    const f = new DataView(new ArrayBuffer(14 + 20 + 20 + len));
    f.setUint16(12, 0x0800);
    f.setUint8(14, 0x45); f.setUint16(16, 40 + len); f.setUint8(23, 6);
    src.forEach((b, j) => f.setUint8(26 + j, b)); dst.forEach((b, j) => f.setUint8(30 + j, b));
    const fromClient = src === C;
    f.setUint16(34, fromClient ? cport : sport); f.setUint16(36, fromClient ? sport : cport);
    f.setUint32(38, seq); f.setUint32(42, ack);
    f.setUint8(46, 5 << 4); f.setUint8(47, flags); f.setUint16(48, 65535);
    return { t, bytes: new Uint8Array(f.buffer) };
  });
  const size = 24 + frames.reduce((n, f) => n + 16 + f.bytes.length, 0);
  const v = new DataView(new ArrayBuffer(size)), le = littleEndian;
  v.setUint32(0, 0xa1b2c3d4, le); v.setUint16(4, 2, le); v.setUint16(6, 4, le);
  v.setUint32(16, 262144, le); v.setUint32(20, 1, le);
  let o = 24;
  for (const f of frames) {
    v.setUint32(o, 1700000000, le); v.setUint32(o + 4, Math.round(f.t * 1e6), le);
    v.setUint32(o + 8, f.bytes.length, le); v.setUint32(o + 12, f.bytes.length, le);
    new Uint8Array(v.buffer, o + 16).set(f.bytes);
    o += 16 + f.bytes.length;
  }
  return v.buffer;
}

for (const le of [true, false]) {
  test(`HTTP 한 번의 흐름 (${le ? "리틀" : "빅"}엔디언 파일)`, () => {
    const pkts = parsePcap(fakePcap(le));
    assert.equal(pkts.length, 8);
    assert.equal(pkts[4].len, 50);
    assert.ok(Math.abs(pkts[7].t - 0.007) < 1e-9);

    const flows = groupFlows(pkts);
    assert.equal(flows.length, 1);
    const f = flows[0];
    assert.equal(f.client, "10.88.0.3:40000");
    assert.deepEqual(f.packets.map(p => p.label),
      ["SYN", "SYN-ACK", "ACK", "데이터 20B", "데이터 50B", "FIN", "FIN", "ACK"]);
    assert.deepEqual(f.packets.map(p => p.phase),
      ["handshake", "handshake", "handshake", "data", "data", "close", "close", "close"]);
    // 상대 seq/ack: 응답 데이터는 서버 seq 1, 클라이언트 요청 20바이트를 받았으므로 ack 21
    assert.equal(f.packets[4].rseq, 1);
    assert.equal(f.packets[4].rack, 21);
  });
}

test("pcap이 아니면 에러", () => {
  assert.throws(() => parsePcap(new ArrayBuffer(24)), /pcap 파일이 아닙니다/);
});

test("손실 → 중복 ACK 3번 → 빠른 재전송", () => {
  const A = FLAG.ACK;
  const segs = [
    [C, S, FLAG.SYN, 0, 0, 0],
    [S, C, FLAG.SYN | A, 0, 1, 0],
    [C, S, A, 1, 1, 0],
    [C, S, A, 1, 1, 100],    // 1~100
    [C, S, A, 101, 1, 100],  // 101~200: 사라짐
    [S, C, A, 1, 101, 0],
    [C, S, A, 201, 1, 100],
    [S, C, A, 1, 101, 0],    // 중복 #1
    [C, S, A, 301, 1, 100],
    [S, C, A, 1, 101, 0],    // 중복 #2
    [C, S, A, 401, 1, 100],
    [S, C, A, 1, 101, 0],    // 중복 #3
    [C, S, A, 101, 1, 100],  // 빠른 재전송
    [S, C, A, 1, 501, 0],
    [C, S, A, 501, 1, 100],
    [C, S, A, 501, 1, 100, 0.3],  // 200ms 넘게 조용하다가 다시 보냄 = 타임아웃 재전송
  ];
  const f = groupFlows(parsePcap(fakePcap(true, segs)))[0];
  const p = f.packets;
  assert.equal(p[4].lost, true);
  assert.deepEqual([p[7].dup, p[9].dup, p[11].dup], [1, 2, 3]);
  assert.equal(p[5].dup, undefined);
  assert.equal(p[12].retrans, "fast");
  assert.equal(p[12].label, "데이터 100B + 빠른 재전송");
  assert.equal(p[15].retrans, "timeout");
  assert.deepEqual(f.stats, { retrans: 2, fast: 1, timeout: 1, recovery: 0, dupAcks: 3, lost: 2 });
  // 1~500을 보냈고 100까지 ACK를 받았으니 전송 중인 바이트는 400
  assert.equal(p[10].flight, 400);
});
