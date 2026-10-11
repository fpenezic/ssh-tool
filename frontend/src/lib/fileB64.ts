// fileToBase64 reads a picked file as base64 (no data: prefix) - the
// shape Go's []byte takes over the Wails IPC.
export function fileToBase64(f: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => {
      const s = String(r.result ?? "");
      resolve(s.slice(s.indexOf(",") + 1));
    };
    r.onerror = () => reject(r.error ?? new Error("could not read " + f.name));
    r.readAsDataURL(f);
  });
}

// Accepted client certificate files: PKCS#12 bundles and PEM.
export const CERT_FILE_ACCEPT = ".p12,.pfx,.pem,.crt,.cer,.key";
