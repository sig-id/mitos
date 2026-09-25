// Mitos acoustic Wi-Fi v1: 16 kHz, 256-point real OFDM, CP64, 48 DBPSK
// data carriers + four clock pilots. Three time-interleaved repetitions + CRC32.
export const RATE = 16000, N = 256, CP = 64, SYMBOL = 320;
const pilots = new Set([36, 48, 60, 72]);
export function crc32(bytes) {
  let crc = 0xffffffff;
  for (const b of bytes) { crc ^= b; for (let j = 0; j < 8; j++) crc = (crc >>> 1) ^ ((crc & 1) ? 0xedb88320 : 0); }
  return (~crc) >>> 0;
}
export function encode(packet) {
  if (packet.length < 42 || packet.length > 137) throw Error('Invalid frame length');
  const frame = new Uint8Array(160); frame[0] = packet.length; frame[1] = packet.length >>> 8; frame.set(packet, 2);
  new DataView(frame.buffer).setUint32(156, crc32(frame.subarray(0,156)), true);
  const pcm = new Float32Array(512 + 84 * SYMBOL + RATE / 5);
  for (let i = 0; i < 512; i++) {
    const t = i / RATE;
    pcm[i] = .65 * Math.sin(2 * Math.PI * (1700*t + (3800*RATE/512)*t*t/2)) * Math.sin(Math.PI*i/511)**2;
  }
  const signs = new Float64Array(84);
  for (let k = 32; k <= 83; k++) signs[k] = ((k*13 + (k*k)) & 4) ? -1 : 1;
  const waves = Array.from({length:84}, (_, k) => Float64Array.from({length:N}, (_, n) => Math.cos(2*Math.PI*k*n/N)));
  let bit = 0;
  for (let s = 0; s < 84; s++) {
    if (s >= 4) for (let k=32; k<=83; k++) if (!pilots.has(k)) {
      const b = bit++ % 1280;
      if (frame[b>>3] & (1<<(b&7))) signs[k] *= -1;
    }
    const wave = new Float32Array(N);
    for (let n=0; n<N; n++) { let value=0; for (let k=32; k<=83; k++) value+= signs[k]*waves[k][n]; wave[n]=value*.017; }
    const p=512+s*SYMBOL; pcm.set(wave.subarray(N-CP), p); pcm.set(wave, p+CP);
  }
  return pcm;
}
export function unhex(value, length) {
  if (!new RegExp(`^[a-f0-9]{${length*2}}$`, 'i').test(value)) throw Error('Scan a new watch code');
  return Uint8Array.from(value.match(/../g), x=>parseInt(x,16));
}
export function sessionFromFragment(fragment) {
  const q = new URLSearchParams(fragment.replace(/^#/,''));
  if (q.get('v') !== '1') throw Error('Scan a watch setup code');
  const sid=unhex(q.get('s')||'',8), key=unhex(q.get('k')||'',32), ssid=q.get('n')||'';
  if (new TextEncoder().encode(ssid).length>32) throw Error('Invalid network name');
  return {sid,key,ssid};
}
export async function seal(ssid, password, session, nonce=crypto.getRandomValues(new Uint8Array(12))) {
  const enc = new TextEncoder(), name=enc.encode(ssid), pass=enc.encode(password);
  if (!name.length || name.length>32 || ssid.includes('\0')) throw Error('Network name must be 1–32 bytes');
  if (pass.length && (pass.length<8 || pass.length>63 || !/^[\x20-\x7e]+$/.test(password))) throw Error('Password must contain 8–63 ASCII characters');
  if (session.ssid && ssid!==session.ssid) throw Error('Network differs from the watch');
  const plain=new Uint8Array(2+name.length+pass.length); plain.set([name.length,pass.length]); plain.set(name,2); plain.set(pass,2+name.length);
  const header=new Uint8Array(24); header.set(enc.encode('MW01')); header.set(session.sid,4); header.set(nonce,12);
  const key=await crypto.subtle.importKey('raw',session.key,'AES-GCM',false,['encrypt']);
  try {
    const cipher=new Uint8Array(await crypto.subtle.encrypt({name:'AES-GCM',iv:nonce,additionalData:header,tagLength:128},key,plain));
    const packet=new Uint8Array(header.length+cipher.length); packet.set(header); packet.set(cipher,header.length); return packet;
  } finally { plain.fill(0); pass.fill(0); }
}
