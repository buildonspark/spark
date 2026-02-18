const isColorSupported =
  process.env.FORCE_COLOR !== "0" &&
  (process.env.FORCE_COLOR !== undefined || process.stdout.isTTY);

function fmt(open: string, close: string) {
  return (s: string) => (isColorSupported ? `${open}${s}${close}` : s);
}

export const bold = fmt("\x1b[1m", "\x1b[22m");
export const dim = fmt("\x1b[2m", "\x1b[22m");
export const red = fmt("\x1b[31m", "\x1b[39m");
export const green = fmt("\x1b[32m", "\x1b[39m");
export const cyan = fmt("\x1b[36m", "\x1b[39m");
