// Synthetic Vite ondemand.s module fixture.
export function deriveFrameIndices(k) {
  const row = parseInt(k[9], 16);
  const t1 = parseInt(k[14], 16);
  const t2 = parseInt(k[21], 16);
  const t3 = parseInt(k[30], 16);
  return [row, t1, t2, t3];
}
