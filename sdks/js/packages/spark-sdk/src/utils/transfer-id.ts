import { UUID, uuidv7obj } from "uuidv7";

export { UUID };

export function generateTransferId(): UUID {
  return uuidv7obj();
}
