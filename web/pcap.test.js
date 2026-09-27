// node --test web/ 으로 실행한다.
import { test } from "node:test";
import assert from "node:assert/strict";
import { parsePcap, groupFlows, FLAG } from "./pcap.js";

// 3-way 핸드셰이크 → 요청/응답 → 4-way 종료를 담은 가짜 pcap을 만든다.
function fakePcap(littleEndian) {
  const C = [10, 88, 0, 3], S = [10, 88, 0, 2], cport = 40000, sport = 80;
  const segs = [
    [C, S, FLAG.SYN, 1000, 0, 0],
    [S, C, FLAG.SYN | FLAG.ACK, 5000, 1001, 0],
    [C, S, FLAG.ACK, 1001, 5001, 0],
    [C, S, FLAG.PSH | FLAG.ACK, 1001, 5001, 20],
    [S, C, FLAG.PSH | FLAG.ACK, 5001, 1021, 50],
    [C, S, FLAG.FIN | FLAG.ACK, 1021, 5051, 0],
    [S, C, FLAG.FIN | FLAG.ACK, 5051, 1022, 0],
    [C, S, FLAG.ACK, 1022, 5052, 0],
  ];
  const frames = segs.map(([src, dst, flags, seq, ack, len], i) => {
    const f = new DataView(new ArrayBuffer(14 + 20 + 20 + len));
    f.setUint16(12, 0x0800);
    f.setUint8(14, 0x45); f.setUint16(16, 40 + len); f.setUint8(23, 6);
    src.forEach((b, j) => f.setUint8(26 + j, b)); dst.forEach((b, j) => f.setUint8(30 + j, b));
    const fromClient = src === C;
    f.setUint16(34, fromClient ? cport : sport); f.setUint16(36, fromClient ? sport : cport);
    f.setUint32(38, seq); f.setUint32(42, ack);
    f.setUint8(46, 5 << 4); f.setUint8(47, flags); f.setUint16(48, 65535);
    return { t: i * 0.001, bytes: new Uint8Array(f.buffer) };
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
