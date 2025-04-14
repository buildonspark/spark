import { classToPlain, instanceToPlain } from "class-transformer";
import { url } from "inspector";
import { BitcoinTxOut } from "../types/index.ts";
import {
  ReceiptProofDto,
  ReceiptProofType,
  SigReceiptProofDataDto,
  MultisigReceiptProofDataDto,
  EmptyReceiptProofData,
  EmptyReceiptProofDataDto,
  Lrc20TransactionStatusDto,
  Lrc20TransactionDto,
  Lrc20TransactionTypeEnum,
  IssueDataDto,
  TransferDataDto,
  Lrc20Transaction,
  TokenPubkeyInfo,
  TokenPubkeyInfoDto,
} from "../types/index.js";
import { JSONStringify } from "../utils/index.ts";

interface RpcResponse<T> {
  result: T;
  error: any;
  id: number | string; // Match your response structure
}

interface JsonRpcRequest {
  jsonrpc: "2.0";
  method: string;
  params?: any[] | object;
  id: string | number | null;
}

export interface JsonRpcAuth {
  username: string;
  password: string;
}

export class Lrc20JsonRPC {
  private readonly url: string;
  private readonly auth?: JsonRpcAuth;

  constructor(url: string, auth?: JsonRpcAuth) {
    this.url = url;
    this.auth = auth;
  }

  async getTokenPubkeyInfo(tokenPubkey: string): Promise<TokenPubkeyInfo | undefined> {
    let data = await this.makeJsonRpcCall<TokenPubkeyInfoDto | undefined>("gettoken_pubkeyinfo", [tokenPubkey]);

    if (!data) {
      return;
    }

    return TokenPubkeyInfo.fromTokenPubkeyInfoDto(data);
  }

  async getRawLrc20Tx(txId: string): Promise<Lrc20TransactionStatusDto> {
    return await this.makeJsonRpcCall<Lrc20TransactionStatusDto>("getrawlrc20transaction", [txId]);
  }

  async getTxOut(txId: string, index: number, bitcoinApiUrl: string, auth: JsonRpcAuth): Promise<BitcoinTxOut | null> {
    return await this.makeJsonRpcCall("gettxout", [txId, index]);
  }

  async listLrcUtxosForPage(page: number): Promise<Array<Lrc20TransactionDto>> {
    return await this.makeJsonRpcCall<Array<Lrc20TransactionDto>>("listlrc20transactions", [page]);
  }

  async listLrcUtxosByWalletToPage(from: number, innerKey: string): Promise<[Array<Lrc20TransactionDto>, number]> {
    const batchSize = 10; // Increased batch size for more concurrent requests
    let utxos: Array<Lrc20TransactionDto> = [];
    let lastPage = from;
    let utxosPage: Array<Lrc20TransactionDto>;

    do {
      const pagePromises = [];
      for (let i = 0; i < batchSize; i++) {
        pagePromises.push(this.listLrcUtxosForPage(lastPage + i));
      }

      const results = await Promise.all(pagePromises);
      utxosPage = results.flat();
      utxos = [...utxos, ...utxosPage];

      let fullPageCount = results.filter((page) => page.length > 99).length;
      lastPage += results.filter((page) => page.length > 99).length;

      if (fullPageCount != batchSize) {
        break;
      }
    } while (utxosPage.length > 99);

    const filteredUtxos = utxos.filter((utxo) => {
      let lrc20Tx = utxo.tx_type;

      switch (lrc20Tx.type) {
        case Lrc20TransactionTypeEnum.Issue: {
          let data = lrc20Tx.data as IssueDataDto;
          const proofsSet = new Set(Object.values(data.output_proofs));
          for (let proof of proofsSet) {
            if (this.isOwnUtxo(proof as ReceiptProofDto, innerKey)) {
              return true;
            }
          }
          break;
        }
        case Lrc20TransactionTypeEnum.Transfer: {
          let data = lrc20Tx.data as TransferDataDto;
          const proofsSet = new Set(Object.values(data.output_proofs));
          for (let proof of proofsSet) {
            if (this.isOwnUtxo(proof as ReceiptProofDto, innerKey)) {
              return true;
            }
          }
          break;
        }
      }
      return false;
    });

    return [filteredUtxos, lastPage > 0 ? lastPage - 1 : lastPage];
  }

  isOwnUtxo(proof: ReceiptProofDto, innerKey: string): boolean {
    switch (proof.type) {
      case ReceiptProofType.EmptyReceipt: {
        let data = proof.data as EmptyReceiptProofDataDto;

        if (data.inner_key == innerKey) {
          return true;
        }

        break;
      }
      case ReceiptProofType.Sig: {
        let data = proof.data as SigReceiptProofDataDto;

        if (data.inner_key == innerKey) {
          return true;
        }

        break;
      }
      case ReceiptProofType.Multisig: {
        let data = proof.data as MultisigReceiptProofDataDto;

        if (data.inner_keys.find((val) => val == innerKey)) {
          return true;
        }

        break;
      }
    }

    return false;
  }

  sendRawLrc20Tx(rawTx: Lrc20Transaction, maxBurnAmount?: number): Promise<boolean> {
    let tx: Lrc20TransactionDto = Lrc20TransactionDto.fromLrc20Transaction(rawTx);
    let params: any[] = [tx];
    if (maxBurnAmount) {
      params.push(maxBurnAmount);
    }
    return this.makeJsonRpcCall("sendrawlrc20transaction", params);
  }

  async makeJsonRpcCall<T>(method: string, params: any[]): Promise<T> {
    const id = Math.floor(Math.random() * 100000); // Simple ID generation
    const request: JsonRpcRequest = {
      jsonrpc: "2.0",
      method,
      params,
      id,
    };
    let body = instanceToPlain(request);
    const response = await fetch(this.url, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        ...(this.auth ? { Authorization: `Basic ${this.basicAuth(this.auth)}` } : {}),
      },
      body: JSONStringify(body),
    });

    if (!response.ok) {
      throw new Error(`RPC call failed: ${response.status}`);
    }

    const data: RpcResponse<T> = await JSON.parse(await response.text());

    if (data.error) {
      throw new Error(`RPC error: ${JSON.stringify(data.error)}`);
    }

    return data.result;
  }

  private basicAuth(auth: JsonRpcAuth): string {
    const { username, password } = auth;
    return Buffer.from(`${username}:${password}`).toString("base64");
  }
}
