import { Query } from "@lightsparkdev/core";

export default class LightsparkClient {
  async executeRawQuery<T>(query: Query<T>): Promise<T | null> {
    throw new Error("Not implemented");
  }
}
