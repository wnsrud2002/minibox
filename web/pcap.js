// pcap 파서: 글로벌 헤더 → 패킷 헤더 → Ethernet → IPv4 → TCP.
// 브라우저와 node 양쪽에서 쓰려고 의존성 없는 ES 모듈로 둔다.

const FLAG = { FIN: 1, SYN: 2, RST: 4, PSH: 8, ACK: 16 };

// parsePcap은 pcap 파일 바이트에서 TCP 세그먼트만 뽑아낸다.
export function parsePcap(buf) {
  const v = new DataView(buf instanceof ArrayBuffer ? buf : buf.buffer.slice(buf.byteOffset, buf.byteOffset + buf.byteLength));
  if (v.byteLength < 24) throw new Error("pcap 파일이 아닙니다 (너무 짧음)");

  // 매직 넘버로 파일의 바이트 순서와 시간 단위(마이크로초/나노초)를 판별한다.
  // 파일 헤더는 캡처한 기계의 엔디언을 따르고, 네트워크 헤더는 항상 빅엔디언이다.
  const magic = v.getUint32(0, false);
  let le, nano;
  if (magic === 0xa1b2c3d4) [le, nano] = [false, false];
  else if (magic === 0xd4c3b2a1) [le, nano] = [true, false];
  else if (magic === 0xa1b23c4d) [le, nano] = [false, true];
  else if (magic === 0x4d3cb2a1) [le, nano] = [true, true];
  else throw new Error("pcap 파일이 아닙니다 (pcapng라면 tcpdump -w로 다시 캡처하세요)");
  const linktype = v.getUint32(20, le) & 0xffff;
  if (linktype !== 1) throw new Error(`Ethernet 캡처만 지원합니다 (linktype ${linktype}). tcpdump -i mb0으로 캡처하세요`);

  const packets = [];
  let off = 24, sec0, t0;
  while (off + 16 <= v.byteLength) {
    const sec = v.getUint32(off, le), frac = v.getUint32(off + 4, le);
    const incl = v.getUint32(off + 8, le);
    // 1.7e9초에 마이크로초를 더한 실수는 정밀도가 부족하다. 첫 패킷 기준 초를 먼저 뺀다.
    sec0 ??= sec;
    const ts = (sec - sec0) + frac / (nano ? 1e9 : 1e6);
    t0 ??= ts;
    const t = ts - t0;
    const tcp = parseFrame(v, off + 16, Math.min(incl, v.byteLength - off - 16));
    if (tcp) packets.push({ no: packets.length + 1, t, ...tcp });
    off += 16 + incl;
  }
  return packets;
}

// Ethernet 프레임 하나에서 IPv4/TCP 필드를 읽는다. TCP가 아니면 null.
function parseFrame(v, o, len) {
  if (len < 14 || v.getUint16(o + 12) !== 0x0800) return null; // IPv4만
  const ip = o + 14;
  const ihl = (v.getUint8(ip) & 0x0f) * 4;
  if (v.getUint8(ip + 9) !== 6) return null; // TCP만
  const totalLen = v.getUint16(ip + 2);
  const tcp = ip + ihl;
  if (tcp + 20 > o + len) return null;
  const dataOff = (v.getUint8(tcp + 12) >> 4) * 4;
  const flags = v.getUint8(tcp + 13);
  return {
    src: ipStr(v, ip + 12), dst: ipStr(v, ip + 16),
    sport: v.getUint16(tcp), dport: v.getUint16(tcp + 2),
    seq: v.getUint32(tcp + 4), ack: v.getUint32(tcp + 8),
    flags, win: v.getUint16(tcp + 14),
    // 캡처 길이가 잘려도(snaplen) 실제 길이는 IP 헤더의 total length로 안다
    len: totalLen - ihl - dataOff,
  };
}

function ipStr(v, o) {
  return [0, 1, 2, 3].map(i => v.getUint8(o + i)).join(".");
}

// groupFlows는 세그먼트를 TCP 연결(4-tuple)별로 묶는다. 방향과 상관없이 같은 연결이다.
// client는 SYN(ACK 없음)을 보낸 쪽이고, SYN을 못 봤으면 첫 패킷을 보낸 쪽이다.
export function groupFlows(packets) {
  const flows = new Map();
  for (const p of packets) {
    const a = `${p.src}:${p.sport}`, b = `${p.dst}:${p.dport}`;
    const key = a < b ? `${a} ${b}` : `${b} ${a}`;
    let f = flows.get(key);
    if (!f) flows.set(key, f = { key, client: a, server: b, packets: [] });
    if (p.flags & FLAG.SYN && !(p.flags & FLAG.ACK)) [f.client, f.server] = [a, b];
    f.packets.push(p);
  }
  for (const f of flows.values()) annotate(f);
  return [...flows.values()];
}

// annotate는 패킷마다 방향, 상대 seq/ack(Wireshark처럼 ISN을 0으로), 라벨, 단계를 붙인다.
function annotate(f) {
  const isn = {};
  for (const p of f.packets) {
    const from = `${p.src}:${p.sport}`;
    p.dir = from === f.client ? "c2s" : "s2c";
    isn[p.dir] ??= p.seq;
  }
  let phase = "handshake";
  for (const p of f.packets) {
    const other = p.dir === "c2s" ? "s2c" : "c2s";
    p.rseq = (p.seq - isn[p.dir]) >>> 0;
    p.rack = p.flags & FLAG.ACK && isn[other] !== undefined ? (p.ack - isn[other]) >>> 0 : null;
    p.label = label(p);
    if (p.flags & (FLAG.FIN | FLAG.RST)) phase = "close";
    else if (phase === "handshake" && p.len > 0) phase = "data";
    p.phase = phase;
  }
}

function label(p) {
  const f = p.flags, names = [];
  if (f & FLAG.SYN) names.push(f & FLAG.ACK ? "SYN-ACK" : "SYN");
  if (f & FLAG.FIN) names.push("FIN");
  if (f & FLAG.RST) names.push("RST");
  if (p.len > 0) names.push(`데이터 ${p.len}B`);
  if (!names.length && f & FLAG.ACK) names.push("ACK");
  return names.join(" + ");
}

export { FLAG };
