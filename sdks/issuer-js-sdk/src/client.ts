export class IssuerSDK {
  /**
   * Creates a new token with the specified parameters
   */
  async createToken({
    tokenName,
    tokenTicker,
    network,
    maxSupply,
    decimals,
    isFreezeable,
    tokenLogo,
    LR,
  }: CreateTokenInput): Promise<any> {
    throw new Error("Not implemented");
  }

  /**
   * Mints new tokens to the specified address
   */
  async mintToken({
    tokenPublicKey,
    amountToMint,
    mintDestinationAddress,
    network,
  }: MintTokenInput): Promise<any> {
    throw new Error("Not implemented");
  }

  /**
   * Transfers tokens to the specified address
   */
  async transferToken({
    tokenPublicKey,
    amountToTransfer,
    transferDestinationAddress,
    network,
  }: TransferTokenInput): Promise<any> {
    throw new Error("Not implemented");
  }

  /**
   * Freezes tokens at the specified address
   */
  async freezeToken({
    tokenPublicKey,
    freezeAddress,
    network,
  }: FreezeTokenInput): Promise<any> {
    throw new Error("Not implemented");
  }

  /**
   * Gets token information by ID
   */
  async getToken(tokenId: string): Promise<any> {
    throw new Error("Not implemented");
  }

  /**
   * Gets the current account information
   */
  async getCurrentAccount(): Promise<any> {
    throw new Error("Not implemented");
  }

  /**
   * @returns Whether or not the client is authorized.
   */
  async isAuthorized(): Promise<boolean> {
    throw new Error("Not implemented");
  }
}
