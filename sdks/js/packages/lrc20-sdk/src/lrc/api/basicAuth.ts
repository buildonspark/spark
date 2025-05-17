export interface BasicAuth {
  username: string;
  password: string;
}

export function basicAuth(auth: BasicAuth): string {
  const { username, password } = auth;
  return Buffer.from(`${username}:${password}`).toString("base64");
}
