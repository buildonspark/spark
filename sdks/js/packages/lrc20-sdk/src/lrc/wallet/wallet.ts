import { address, crypto, networks, payments, Psbt, script, Transaction } from "bitcoinjs-lib";
import { instanceToPlain, plainToInstance } from "class-transformer";
import { ECPairInterface } from "ecpair";
import { publicKeyToAddress } from "../../address/index.ts";
import { NetworkType, toNetworkType, toPsbtNetwork } from "../../network/index.ts";
import { AddressType } from "../../types.ts";
import { ElectrsApi, Lrc20JsonRPC, BasicAuth } from "../api/index.ts";
import { BtcUtxosCoinSelection, Lrc20UtxosCoinSelection } from "../coinselection/index.ts";
import { TransactionBuilder } from "../transaction/index.ts";
import {
  BitcoinTransactionDto,
  BtcMetadata,
  BitcoinUtxo,
  Lrc20Utxo,
  TokenPubkey,
  TokenPubkeyInfo,
  BitcoinInput,
  MultisigReceiptInput,
  ReceiptInput,
  TxInput,
  TokenAmount,
  BitcoinOutput,
  MultisigReceiptOutput,
  ReceiptOutput,
  SparkExitOutput,
  TxOutput,
  Payment,
  Receipt,
  getReceiptDataFromProof,
  ReceiptProof,
  ReceiptProofType,
  SigReceiptProofData,
  AnnouncementData,
  TokenPubkeyAnnouncement,
  TxFreezeAnnouncement as TxFreezeAnnouncement,
  TransferData,
  TransferOwnershipAnnouncement,
  Lrc20Transaction,
  Lrc20TransactionDto,
  Lrc20TransactionType,
  Lrc20TransactionTypeEnum,
  PubkeyFreezeAnnouncement,
  SparkExitMetadata,
  ElectrsTransactionOutput,
} from "../types/index.ts";
import {
  filterUniqueUtxo,
  reverseBuffer,
  toEvenParity,
  toXOnly,
  DUST_AMOUNT,
  EMPTY_TOKEN_PUBKEY,
  LRC_NODE_URL,
  ELECTRS_URL,
  JSONParse,
  JSONStringify,
  JSONStringifyBodyDown,
} from "../utils/index.ts";
import { ECPair } from "../../bitcoin-core.ts";

export interface LRC20WalletApiConfig {
  lrc20NodeUrl: string;
  electrsUrl: string;
  electrsCredentials?: BasicAuth;
}

export class LRCWallet {
  public p2trAddress: string;
  public p2wpkhAddress: string;
  public addressInnerKey: Buffer;
  public pubkey: Buffer;
  public btcUtxos: Array<Lrc20Utxo | BitcoinUtxo> = [];
  public spentBtcUtxos: Array<Lrc20Utxo | BitcoinUtxo> = [];
  public lrc20Utxos: Array<Lrc20Utxo> = [];
  public spentLrc20Utxos: Array<Lrc20Utxo> = [];

  private emptyLrc20Utxos: Array<Lrc20Utxo> = [];
  private lastLrc20Page = 0;
  private unspentBtcUtxo: Array<Lrc20Utxo | BitcoinUtxo> = [];
  private networkType: NetworkType;
  private builder: TransactionBuilder;
  private keyPair: ECPairInterface;
  private network: networks.Network;
  private electrsApi: ElectrsApi;
  private lrcNodeApi: Lrc20JsonRPC;
  private tokenInfoMap: Map<string, TokenPubkeyInfo> = new Map();

  private readonly privateKeyHex: string;

  constructor(
    privateKeyHex: string,
    btcNetwork: networks.Network,
    networkType: NetworkType,
    apiConfig?: LRC20WalletApiConfig,
  ) {
    this.privateKeyHex = privateKeyHex;
    this.network = btcNetwork;
    this.networkType = networkType;

    this.init(apiConfig);
  }

  public getNetwork(): networks.Network {
    return this.network;
  }

  public getKeypair(): ECPairInterface {
    return this.keyPair;
  }

  public getElectrsApi(): ElectrsApi {
    return this.electrsApi;
  }

  protected init(apiConfig?: LRC20WalletApiConfig) {
    this.keyPair = ECPair.fromPrivateKey(Buffer.from(this.privateKeyHex, "hex"), { network: this.network });
    this.pubkey = this.keyPair.publicKey;
    this.builder = new TransactionBuilder(this.keyPair, this.network);
    this.addressInnerKey = this.keyPair.publicKey;

    const bech32 = this.network.bech32;

    this.electrsApi = apiConfig
      ? new ElectrsApi(apiConfig.electrsUrl, apiConfig.electrsCredentials)
      : new ElectrsApi(ELECTRS_URL[this.networkType] || ELECTRS_URL["default"], null);
    this.lrcNodeApi = apiConfig
      ? new Lrc20JsonRPC(apiConfig.lrc20NodeUrl)
      : new Lrc20JsonRPC(LRC_NODE_URL[this.networkType] || LRC_NODE_URL["default"]);

    const pubkeyHex = this.keyPair.publicKey.toString("hex");
    const networkType = toNetworkType(this.network);

    this.p2trAddress = publicKeyToAddress(pubkeyHex, AddressType.P2TR, networkType);
    this.p2wpkhAddress = publicKeyToAddress(pubkeyHex, AddressType.P2WPKH, networkType);
  }

  public async syncWallet(): Promise<void> {
    const allTimeStartTime = new Date();

    // Get bitcoin utxos
    const bech32Address = payments.p2wpkh({
      pubkey: this.keyPair.publicKey,
      network: this.network,
    }).address!;

    let btcUtxos = await this.fetchUtxos(bech32Address);

    btcUtxos = await Promise.all(
      btcUtxos.map(async (utxo) => ({
        ...utxo,
        hex: await this.electrsApi.getTransactionHex(utxo.txid),
      })),
    );

    this.btcUtxos = btcUtxos;

    const allTimeEndTime = new Date();
    console.log(`Time taken for all: ${allTimeEndTime.getTime() - allTimeStartTime.getTime()}ms`);
  }

  private async fetchUtxos(address: string): Promise<BitcoinUtxo[]> {
    let txs = await this.electrsApi.listTransactions(address);

    let inputs = [];
    let outputs = new Map<{ txid: string; vout: number }, ElectrsTransactionOutput>();
    let txStatuses = {};
    txs.forEach((tx) => {
      inputs = [...inputs, ...tx.vin];

      for (let i = 0; i < tx.vout.length; i++) {
        if (tx.vout[i].scriptpubkey_address == address) {
          let key = { txid: tx.txid, vout: i };
          outputs.set(key, tx.vout[i]);
        }
      }
      txStatuses[tx.txid] = tx.status;
    });

    let utxos: BitcoinUtxo[] = [];
    for (let [{ txid, vout }, output] of outputs.entries()) {
      let isSpent = inputs.findIndex((input) => input.txid == txid && input.vout == vout) != -1;
      if (isSpent) {
        continue;
      }

      let utxo = new BitcoinUtxo(txid, BigInt(vout), output.value, txStatuses[txid]);
      utxos.push(utxo);
    }

    return utxos;
  }

  public changeNetwork(network: NetworkType): void {
    this.networkType = network;
    this.network = toPsbtNetwork(network);

    this.init();

    // Clear UTXO arrays and reset lastLrc20Page
    this.lrc20Utxos = [];
    this.spentLrc20Utxos = [];
    this.emptyLrc20Utxos = [];
    this.btcUtxos = [];
    this.spentBtcUtxos = [];
    this.unspentBtcUtxo = [];
    this.lastLrc20Page = 0;
  }

  public getLrc20Utxos(): Lrc20Utxo[] {
    return this.lrc20Utxos;
  }

  public getSpentBtcUtxos(): Array<Lrc20Utxo | BitcoinUtxo> {
    return this.spentBtcUtxos;
  }

  public getSpentLrc20Utxos(): Lrc20Utxo[] {
    return this.spentLrc20Utxos;
  }

  public getBtcUtxos(): Array<Lrc20Utxo | BitcoinUtxo> {
    return this.btcUtxos;
  }

  public getBtcBalance(): number {
    return this.btcUtxos.reduce((acc, utxo) => acc + utxo.satoshis, 0);
  }

  public getTokenPubkeyInfo(tokenPubkey: string): Promise<TokenPubkeyInfo> {
    return this.lrcNodeApi.getTokenPubkeyInfo(tokenPubkey);
  }

  public async getTokenPubkeyInfoForWallet(tokenPubkey: string): Promise<any> {
    const lrc20Balances = await this.getLrc20Balances();

    const balance = lrc20Balances.find((balance) => balance.tokenPubkey === tokenPubkey);

    const tokenPubkeyInfo = this.tokenInfoMap.get(tokenPubkey)!;

    return {
      lrcBalance: {
        tokenPubkey: tokenPubkey,
        balance: balance ? Number(balance.balance) : 0,
      },
      lrcInfo: {
        tokenPubkey: tokenPubkey,
        name: tokenPubkeyInfo.announcement?.name ?? `${tokenPubkey.slice(0, 4)}...${tokenPubkey.slice(-4)}`,
        symbol: tokenPubkeyInfo.announcement?.symbol ?? `${tokenPubkey.slice(0, 3)}`,
        decimals: tokenPubkeyInfo.announcement?.decimal ?? 0,
        maxSupply: tokenPubkeyInfo.announcement?.maxSupply?.toString() ?? "0",
        totalSupply: tokenPubkeyInfo.totalSupply.toString(),
      },
    };
  }

  public async prepareSendBTC(
    transfers: Array<{ receiverP2WPKH: string; sats: number }>,
    feeRateVb: number,
    locktime = 0,
    sequence = 0,
  ): Promise<Transaction> {
    const satsAmount = transfers.reduce((acc, transfer) => acc + BigInt(transfer.sats), BigInt(0));
    const inputs = await this.createInputsFromUtxos(
      new BtcUtxosCoinSelection(this.btcUtxos).selectUtxos(0, 0, transfers.length, feeRateVb, false, satsAmount),
    );

    const outputs = transfers.map((transfer) => {
      return BitcoinOutput.createFromRaw(transfer.receiverP2WPKH, transfer.sats);
    });

    const changeOutput = this.createRawBtcChangeOutput();

    return this.builder.buildAndSignTransaction(inputs, outputs, changeOutput, feeRateVb, [], locktime, sequence);
  }

  public async prepareAnnouncement(
    announcement: TokenPubkeyAnnouncement,
    feeRateVb: number,
    locktime = 0,
    sequence = 0,
  ): Promise<Lrc20Transaction> {
    const tokenPubkeyAnnouncementOutput = await this.builder.buildAnnouncementOutput(announcement);
    const changeOutput = this.createRawBtcChangeOutput();
    const inputs = await this.createInputsFromUtxos(
      new BtcUtxosCoinSelection(this.btcUtxos).selectUtxos(0, 0, 2, feeRateVb, true),
    );

    const tx = this.builder.buildAndSignTransaction(
      inputs,
      [tokenPubkeyAnnouncementOutput],
      changeOutput,
      feeRateVb,
      [],
      locktime,
      sequence,
    );

    return this.convertToLrc20Transaction(
      inputs,
      [tokenPubkeyAnnouncementOutput, changeOutput],
      tx,
      Lrc20TransactionTypeEnum.Announcement,
      announcement,
    );
  }

  public async prepareTransferOwnership(
    announcement: TransferOwnershipAnnouncement,
    feeRateVb: number,
    locktime = 0,
    sequence = 0,
  ): Promise<Lrc20Transaction> {
    const transferOwnershipOutput = await this.builder.buildTransferOwnershipOutput(announcement);
    const changeOutput = this.createRawBtcChangeOutput();
    const inputs = await this.createInputsFromUtxos(
      new BtcUtxosCoinSelection(this.btcUtxos).selectUtxos(0, 0, 2, feeRateVb, true),
    );

    const tx = this.builder.buildAndSignTransaction(
      inputs,
      [transferOwnershipOutput],
      changeOutput,
      feeRateVb,
      [],
      locktime,
      sequence,
    );

    return this.convertToLrc20Transaction(
      inputs,
      [transferOwnershipOutput, changeOutput],
      tx,
      Lrc20TransactionTypeEnum.Announcement,
      announcement,
    );
  }

  public async prepareAnnounceWithFee(
    announcement: TokenPubkeyAnnouncement,
    feeRateVb: number,
    btcAddress: string,
    btcAmount: number,
    locktime = 0,
    sequence = 0,
  ): Promise<Lrc20Transaction> {
    const tokenPubkeyAnnouncementOutput = await this.builder.buildAnnouncementOutput(announcement);
    const changeOutput = this.createReceiptBtcChangeOutput();
    const btcOutput = BitcoinOutput.createFromRaw(btcAddress, btcAmount);
    const inputs = await this.createInputsFromUtxos(
      new BtcUtxosCoinSelection(this.btcUtxos).selectUtxos(0, 0, 3, feeRateVb),
    );

    const tx = this.builder.buildAndSignTransaction(
      inputs,
      [tokenPubkeyAnnouncementOutput, btcOutput],
      changeOutput,
      feeRateVb,
      [],
      locktime,
      sequence,
    );

    return this.convertToLrc20Transaction(
      inputs,
      [tokenPubkeyAnnouncementOutput, btcOutput, changeOutput],
      tx,
      Lrc20TransactionTypeEnum.Announcement,
      announcement,
    );
  }

  public async prepareFreeze(
    freeze: TxFreezeAnnouncement | PubkeyFreezeAnnouncement,
    feeRateVb: number,
    locktime = 0,
    sequence = 0,
  ): Promise<Lrc20Transaction> {
    const freezeAnnouncementOutput = await this.builder.buildFreezeOutput(freeze);
    const changeOutput = this.createRawBtcChangeOutput();
    const inputs = await this.createInputsFromUtxos(
      new BtcUtxosCoinSelection(this.btcUtxos).selectUtxos(0, 0, 2, feeRateVb, true),
    );

    const tx = this.builder.buildAndSignTransaction(
      inputs,
      [freezeAnnouncementOutput],
      changeOutput,
      feeRateVb,
      [],
      locktime,
      sequence,
    );

    return this.convertToLrc20Transaction(
      inputs,
      [freezeAnnouncementOutput, changeOutput],
      tx,
      Lrc20TransactionTypeEnum.Announcement,
      freeze,
    );
  }

  public async prepareIssuance(tokens: Array<Payment>, feeRateVb: number): Promise<Lrc20Transaction> {
    const inputs = await this.createInputsFromUtxos(
      new BtcUtxosCoinSelection(this.btcUtxos).selectUtxos(0, 0, tokens.length + 2, feeRateVb),
    );

    return this.prepareIssuanceDefineInputs(inputs, tokens, feeRateVb);
  }

  public async prepareIssuanceDefineInputs(
    inputs: Array<TxInput>,
    tokens: Array<Payment>,
    feeRateVb: number,
    locktime = 0,
    sequence = 0,
  ): Promise<Lrc20Transaction> {
    const issuanceOutputs = this.createOutputs(tokens);
    const issuanceAnnouncementOutput = await this.builder.buildIssuanceOutput(issuanceOutputs);
    const changeOutput = this.createReceiptBtcChangeOutput();

    const tx = this.builder.buildAndSignTransaction(
      inputs,
      [issuanceAnnouncementOutput, ...issuanceOutputs],
      changeOutput,
      feeRateVb,
      [],
      locktime,
      sequence,
    );

    return this.convertToLrc20Transaction(
      inputs,
      [issuanceAnnouncementOutput, ...issuanceOutputs, changeOutput],
      tx,
      Lrc20TransactionTypeEnum.Issue,
    );
  }

  public async prepareSparkExit(tokens: Array<Payment>, feeRateVb: number): Promise<Lrc20Transaction> {
    const inputs = await this.createInputsFromUtxos(
      new BtcUtxosCoinSelection(this.btcUtxos).selectUtxos(0, 0, tokens.length + 1, feeRateVb),
    );

    return this.prepareSparkExitDefineInputs(inputs, tokens, feeRateVb);
  }

  public async prepareSparkExitDefineInputs(
    inputs: Array<TxInput>,
    tokens: Array<Payment>,
    feeRateVb: number,
    locktime = 0,
    sequence = 0,
  ): Promise<Lrc20Transaction> {
    const exitOutputs = this.createOutputs(tokens);
    const changeOutput = this.createReceiptBtcChangeOutput();

    const tx = this.builder.buildAndSignTransaction(
      inputs,
      exitOutputs,
      changeOutput,
      feeRateVb,
      [],
      locktime,
      sequence,
    );

    return this.convertToLrc20Transaction(
      inputs,
      [...exitOutputs, changeOutput],
      tx,
      Lrc20TransactionTypeEnum.SparkExit,
    );
  }

  public async prepareTransfer(
    tokens: Array<Payment>,
    feeRateVb: number,
    locktime = 0,
    sequence = 0,
  ): Promise<Lrc20Transaction> {
    const lrc20Outputs = this.createOutputs(tokens);
    const selectedLrc20Utxos = new Lrc20UtxosCoinSelection(this.lrc20Utxos).selectUtxos(tokens);
    const lrc20Inputs = await this.createInputsFromUtxos(selectedLrc20Utxos);
    const changeBtcOutput = this.createReceiptBtcChangeOutput();
    const changeLrc20Outputs = this.createLrc20ChangeOutputs(lrc20Inputs, lrc20Outputs);
    const btcInputs = await this.createInputsFromUtxos(
      new BtcUtxosCoinSelection(this.btcUtxos).selectUtxos(
        0,
        [...lrc20Inputs].length,
        [...lrc20Outputs, ...changeLrc20Outputs, changeBtcOutput].length,
        feeRateVb,
      ),
    );

    const tx = this.builder.buildAndSignTransaction(
      [...btcInputs, ...lrc20Inputs],
      [...lrc20Outputs, ...changeLrc20Outputs],
      changeBtcOutput,
      feeRateVb,
      [],
      locktime,
      sequence,
    );

    return this.convertToLrc20Transaction(
      [...btcInputs, ...lrc20Inputs],
      [...lrc20Outputs, ...changeLrc20Outputs, changeBtcOutput],
      tx,
      Lrc20TransactionTypeEnum.Transfer,
    );
  }

  public async prepareTransferDefineInputs(
    tokens: Array<Payment>,
    feeRateVb: number,
    btcInputs: Array<BitcoinInput>,
    lrc20Inputs: Array<ReceiptInput>,
    locktime = 0,
    sequence = 0,
  ): Promise<Lrc20Transaction> {
    const lrc20Outputs = this.createOutputs(tokens);

    const changeBtcOutput = this.createReceiptBtcChangeOutput();
    const changeLrc20Outputs = this.createLrc20ChangeOutputs(lrc20Inputs, lrc20Outputs);

    const tx = this.builder.buildAndSignTransaction(
      [...btcInputs, ...lrc20Inputs],
      [...lrc20Outputs, ...changeLrc20Outputs],
      changeBtcOutput,
      feeRateVb,
      [],
      locktime,
      sequence,
    );

    return this.convertToLrc20Transaction(
      [...btcInputs, ...lrc20Inputs],
      [...lrc20Outputs, ...changeLrc20Outputs, changeBtcOutput],
      tx,
      Lrc20TransactionTypeEnum.Transfer,
    );
  }

  public async prepareMultisigTransfer(
    txid: string,
    privateKeys: Array<ECPairInterface>,
    tokens: Array<Payment>,
    feeRateVb: number,
    locktime = 0,
    sequence = 0,
  ): Promise<Lrc20Transaction> {
    const lrc20Outputs = this.createOutputs(tokens);
    const multisigInputTx = await this.lrcNodeApi.getRawLrc20Tx(txid);
    const [utxos, _] = Lrc20Utxo.fromLrc20Transaction(Lrc20Transaction.fromLrc20TransactionDto(multisigInputTx.data));
    const p2wshUtxos = utxos.filter((utxo) => utxo.receipt.type == ReceiptProofType.P2WSH);
    const lrc20Inputs = await this.createInputsFromUtxos(p2wshUtxos);
    const changeBtcOutput = this.createReceiptBtcChangeOutput();
    const changeLrc20Outputs = this.createLrc20ChangeOutputs(lrc20Inputs, lrc20Outputs);
    const btcInputs = await this.createInputsFromUtxos(
      new BtcUtxosCoinSelection(this.btcUtxos).selectUtxos(
        0,
        [...lrc20Inputs].length,
        [...lrc20Outputs, ...changeLrc20Outputs, changeBtcOutput].length,
        feeRateVb,
        false,
      ),
    );

    const tx = this.builder.buildAndSignTransaction(
      [...btcInputs, ...lrc20Inputs],
      [...lrc20Outputs, ...changeLrc20Outputs],
      changeBtcOutput,
      feeRateVb,
      privateKeys,
      locktime,
      sequence,
    );

    return this.convertToLrc20Transaction(
      [...btcInputs, ...lrc20Inputs],
      [...lrc20Outputs, ...changeLrc20Outputs, changeBtcOutput],
      tx,
      Lrc20TransactionTypeEnum.Transfer,
    );
  }

  public async prepareSingleInputTx(
    giveData: { amount: bigint; tokenPubkey: string },
    feeRateVb: number,
  ): Promise<Lrc20Transaction> {
    let selectedUtxos: Array<Lrc20Utxo | BitcoinUtxo> = [];

    if (giveData.tokenPubkey) {
      selectedUtxos = new Lrc20UtxosCoinSelection(this.lrc20Utxos).selectUtxos([
        {
          amount: giveData.amount,
          tokenPubkey: giveData.tokenPubkey,
        },
      ]);
    } else {
      selectedUtxos = new BtcUtxosCoinSelection(this.btcUtxos).selectUtxos(0, 0, 1, feeRateVb, false, giveData.amount);
    }

    const inputs = await this.createInputsFromUtxos(selectedUtxos);

    if (inputs.length > 1) {
      throw new Error(
        `Amount of inputs (${inputs.length}) is greater than expected (1). Perhaps, you forgot to prepare an output`,
      );
    }

    const tx = this.builder.buildAndSignOneInputTx(inputs[0]);

    return this.convertToLrc20Transaction([...inputs], [], tx, Lrc20TransactionTypeEnum.Transfer);
  }

  public async prepareTakerPsbt(
    giveData: { amount: bigint; tokenPubkey: string },
    receiveData: { amount: bigint; tokenPubkey: string },
    allowSendMore = false,
    feeRateVb?: number,
    total_tx_fee?: number,
  ): Promise<Lrc20Transaction> {
    await this.syncWallet();

    const p2trAddress = payments.p2tr({
      network: this.network,
      pubkey: toXOnly(this.keyPair.publicKey),
    }).address!;

    let lrc20Outputs: TxOutput[] = [];
    let selectedLrc20Utxos: Array<Lrc20Utxo | BitcoinUtxo> = [];
    let lrc20InputSum = BigInt(0);

    if (receiveData.tokenPubkey) {
      lrc20Outputs = this.createOutputs([
        {
          recipient: p2trAddress,
          amount: receiveData.amount,
          tokenPubkey: receiveData.tokenPubkey,
        },
      ]);
    } else {
      lrc20Outputs = [new BitcoinOutput(Buffer.from(p2trAddress, "hex"), Number(receiveData.amount))];
    }

    if (giveData.tokenPubkey) {
      selectedLrc20Utxos = new Lrc20UtxosCoinSelection(this.lrc20Utxos).selectUtxos([
        {
          amount: giveData.amount,
          tokenPubkey: giveData.tokenPubkey,
        },
      ]);

      selectedLrc20Utxos.map((input) => {
        if ("receipt" in input) {
          lrc20InputSum += input.receipt.data.receipt.tokenAmount.amount;
        }
      });

      if (!allowSendMore && lrc20InputSum > giveData.amount) {
        throw new Error(
          `You are about to send ${
            lrc20InputSum - giveData.amount
          } more tokens. Provide allowSendMore = true argument to send more tokens then you want to.`,
        );
      }
    } else {
      if (feeRateVb != undefined) {
        selectedLrc20Utxos = new BtcUtxosCoinSelection(this.btcUtxos).selectUtxos(
          0,
          0,
          1,
          feeRateVb,
          false,
          giveData.amount,
        );
      } else {
        selectedLrc20Utxos = new BtcUtxosCoinSelection(
          this.btcUtxos,
          // Including extra input and extra output
        ).coinSelection(BigInt(total_tx_fee), 148n + 34n);
      }
    }

    if (total_tx_fee != undefined) {
      if (lrc20Outputs[0].satoshis - total_tx_fee > 0) {
        lrc20Outputs[0].satoshis -= total_tx_fee;
      } else {
        throw new Error(`Cannot spent more satoshis on fees than you have`);
      }
    }

    const inputs = await this.createInputsFromUtxos(selectedLrc20Utxos);

    const tx = this.builder.buildAndSignMakerPsbt([...inputs], lrc20Outputs[0]);

    return this.convertToLrc20Transaction([...inputs], [lrc20Outputs[0]], tx, Lrc20TransactionTypeEnum.Transfer);
  }

  public async signPsbtMaker(transactions: Lrc20Transaction[], feeRateVb: number): Promise<Lrc20Transaction> {
    await this.syncWallet();

    const psbt = new Psbt({ network: this.network });
    psbt.setVersion(2);
    psbt.setLocktime(0);

    let takerInputs: TxInput[] = [];
    let takerOutputs: ReceiptOutput[] = [];
    let makerReceival: Map<TokenPubkey, BigInt> = new Map();
    let takerTokenPubkey: TokenPubkey = new TokenPubkey();
    let takerAmount: BigInt = BigInt(0);
    for (const transaction of transactions) {
      // Add received psbt inputs to the taker psbt structure
      const txHash = Buffer.from(transaction.bitcoin_tx.ins[0].hash);
      const txId = reverseBuffer(txHash);
      let tx = await this.lrcNodeApi.getRawLrc20Tx(txId.toString("hex"));

      let parsedTx = new BitcoinTransactionDto(
        tx.data.bitcoin_tx.version,
        tx.data.bitcoin_tx.lock_time,
        tx.data.bitcoin_tx.input,
        tx.data.bitcoin_tx.output,
      );
      const txHex = BitcoinTransactionDto.toTransaction(parsedTx).toHex();

      psbt.addInput({
        hash: txHash,
        index: transaction.bitcoin_tx.ins[0].index,
        nonWitnessUtxo: Buffer.from(txHex, "hex"),
      });

      if (transaction.tx_type.type === Lrc20TransactionTypeEnum.Transfer) {
        const input_proof = transaction.tx_type.data.input_proofs.get(0);

        if (input_proof.type === ReceiptProofType.Sig) {
          takerInputs.push(
            new ReceiptInput(
              txHash.toString("hex"),
              transaction.bitcoin_tx.ins[0].index,
              txHex,
              tx.data.bitcoin_tx.output[transaction.bitcoin_tx.ins[0].index].value,
              input_proof.data.receipt,
              input_proof.data.innerKey!,
            ),
          );
        }
      }

      // Add received psbt outputs to the taker psbt structure
      psbt.addOutput({
        script: transaction.bitcoin_tx.outs[0].script,
        value: transaction.bitcoin_tx.outs[0].value,
      });

      if (transaction.tx_type.type !== Lrc20TransactionTypeEnum.Transfer) {
        throw new Error("Received psbt is not transfer");
      }

      const makerInputReceipt = transaction.tx_type.data.input_proofs.get(0)!.data.receipt;
      let tokensAmount = makerReceival.get(makerInputReceipt.tokenPubkey);
      if (tokensAmount === undefined) {
        tokensAmount = 0n;
      }
      makerReceival.set(makerInputReceipt.tokenPubkey, tokensAmount.valueOf() + makerInputReceipt.tokenAmount.amount);

      // Form maker's inputs so he can suttisfy taker's output
      const takerOutputProof = transaction.tx_type.data.output_proofs.get(0)!;
      const takerOutReceipt = takerOutputProof.data.receipt!;
      takerTokenPubkey = takerOutReceipt.tokenPubkey;
      takerAmount = takerAmount.valueOf() + takerOutReceipt.tokenAmount.amount;

      if (takerOutputProof.type == ReceiptProofType.Sig) {
        takerOutputs.push(
          new ReceiptOutput(
            Buffer.from(takerOutputProof.data.innerKey, "hex"),
            transaction.bitcoin_tx.outs[0].value,
            new Receipt(takerOutputProof.data.receipt.tokenAmount, takerOutputProof.data.receipt.tokenPubkey),
          ),
        );
      }
    }

    const selectedLrc20Utxos = new Lrc20UtxosCoinSelection(this.lrc20Utxos).selectUtxos([
      {
        amount: takerAmount.valueOf(),
        tokenPubkey: takerTokenPubkey.inner.toString("hex"),
      },
    ]);

    // Form outputs that spend takers' inputs
    let makerOuts: ReceiptOutput[] = [];
    makerReceival.forEach((value, tokenPubkey) => {
      makerOuts.push(this.createLrc20ChangeOutput(new Receipt(new TokenAmount(value.valueOf()), tokenPubkey)));
    });
    const changeBtcOutput = this.createReceiptBtcChangeOutput();

    // Form makers's change from selected input
    const takerLrc20InputSum = selectedLrc20Utxos.reduce(
      (accumulator, currentValue) => accumulator + currentValue.receipt.data.receipt.tokenAmount.amount,
      BigInt(0),
    );
    if (takerLrc20InputSum - takerAmount.valueOf() > 0) {
      makerOuts.push(
        this.createLrc20ChangeOutput(
          new Receipt(new TokenAmount(takerLrc20InputSum - takerAmount.valueOf()), takerTokenPubkey),
        ),
      );
    }

    const btcInputs = new BtcUtxosCoinSelection(this.btcUtxos).selectUtxos(
      transactions.length,
      takerInputs.length,
      takerOutputs.length + makerOuts.length,
      feeRateVb,
    );
    const makerInputs = await this.createInputsFromUtxos([...selectedLrc20Utxos, ...btcInputs]);

    const tx = this.builder.buildAndSignTakerPsbt(
      psbt,
      takerInputs,
      makerInputs,
      [...makerOuts, changeBtcOutput],
      feeRateVb,
    );

    let lrc20Tx = this.convertToLrc20Transaction(
      [...takerInputs, ...makerInputs],
      [...takerOutputs, ...makerOuts, changeBtcOutput],
      tx,
      Lrc20TransactionTypeEnum.Transfer,
    );

    // Change mocked taker' witnesses to it's real one
    for (let i = 0; i < transactions.length; i++) {
      tx.ins[i].witness = transactions[i].bitcoin_tx.ins[0].witness;
    }

    return lrc20Tx;
  }

  public async signPsbt(
    takerTxs: Lrc20Transaction[],
    makerTx: Lrc20Transaction,
    feeRateVb: number,
  ): Promise<Lrc20Transaction> {
    await this.syncWallet();

    const psbt = new Psbt({ network: this.network });
    psbt.setVersion(2);
    psbt.setLocktime(0);

    let takerInputs: TxInput[] = [];
    let takerOutputs: ReceiptOutput[] = [];
    let makerReceival: Map<TokenPubkey, BigInt> = new Map();
    let takerTokenPubkey: TokenPubkey = new TokenPubkey();
    let takerAmount = 0n;
    for (const transaction of takerTxs) {
      // Add received psbt inputs to the taker psbt structure
      const txHash = Buffer.from(transaction.bitcoin_tx.ins[0].hash);
      const txId = reverseBuffer(txHash);
      let tx = await this.lrcNodeApi.getRawLrc20Tx(txId.toString("hex"));

      let parsedTx = new BitcoinTransactionDto(
        tx.data.bitcoin_tx.version,
        tx.data.bitcoin_tx.lock_time,
        tx.data.bitcoin_tx.input,
        tx.data.bitcoin_tx.output,
      );
      const txHex = BitcoinTransactionDto.toTransaction(parsedTx).toHex();

      psbt.addInput({
        hash: txHash,
        index: transaction.bitcoin_tx.ins[0].index,
        nonWitnessUtxo: Buffer.from(txHex, "hex"),
      });

      if (transaction.tx_type.type === Lrc20TransactionTypeEnum.Transfer) {
        const input_proof = transaction.tx_type.data.input_proofs.get(0);

        if (input_proof.type === ReceiptProofType.Sig) {
          takerInputs.push(
            new ReceiptInput(
              txHash.toString("hex"),
              transaction.bitcoin_tx.ins[0].index,
              txHex,
              tx.data.bitcoin_tx.output[transaction.bitcoin_tx.ins[0].index].value,
              input_proof.data.receipt,
              input_proof.data.innerKey!,
            ),
          );
        }
      }

      // Add received psbt outputs to the taker psbt structure
      psbt.addOutput({
        script: transaction.bitcoin_tx.outs[0].script,
        value: transaction.bitcoin_tx.outs[0].value,
      });

      if (transaction.tx_type.type !== Lrc20TransactionTypeEnum.Transfer) {
        throw new Error("Received psbt is not transfer");
      }

      const makerInputReceipt = transaction.tx_type.data.input_proofs.get(0)!.data.receipt;
      let tokensAmount = makerReceival.get(makerInputReceipt.tokenPubkey);
      if (tokensAmount === undefined) {
        tokensAmount = 0n;
      }
      makerReceival.set(makerInputReceipt.tokenPubkey, tokensAmount.valueOf() + makerInputReceipt.tokenAmount.amount);

      // Form maker's inputs so he can suttisfy taker's output
      const takerOutputProof = transaction.tx_type.data.output_proofs.get(0)!;
      const takerOutReceipt = takerOutputProof.data.receipt!;
      takerTokenPubkey = takerOutReceipt.tokenPubkey;
      takerAmount = takerAmount.valueOf() + takerOutReceipt.tokenAmount.amount;

      if (takerOutputProof.type == ReceiptProofType.Sig) {
        takerOutputs.push(
          new ReceiptOutput(
            Buffer.from(takerOutputProof.data.innerKey, "hex"),
            transaction.bitcoin_tx.outs[0].value,
            new Receipt(takerOutputProof.data.receipt.tokenAmount, takerOutputProof.data.receipt.tokenPubkey),
          ),
        );
      }
    }

    // Add received psbt inputs to the taker psbt structure
    const txHash = Buffer.from(makerTx.bitcoin_tx.ins[0].hash);
    const txId = reverseBuffer(txHash);
    let makerRawTx = await this.lrcNodeApi.getRawLrc20Tx(txId.toString("hex"));

    let parsedTx = new BitcoinTransactionDto(
      makerRawTx.data.bitcoin_tx.version,
      makerRawTx.data.bitcoin_tx.lock_time,
      makerRawTx.data.bitcoin_tx.input,
      makerRawTx.data.bitcoin_tx.output,
    );
    const txHex = BitcoinTransactionDto.toTransaction(parsedTx).toHex();

    psbt.addInput({
      hash: txHash,
      index: makerTx.bitcoin_tx.ins[0].index,
      nonWitnessUtxo: Buffer.from(txHex, "hex"),
    });

    let makerPubKey: Buffer;
    if (makerTx.tx_type.type === Lrc20TransactionTypeEnum.Transfer) {
      const input_proof = makerTx.tx_type.data.input_proofs.get(0);
      if (input_proof.type === ReceiptProofType.Sig) {
        makerPubKey = Buffer.from(input_proof.data.innerKey, "hex");
        takerInputs.push(
          new ReceiptInput(
            txHash.toString("hex"),
            makerTx.bitcoin_tx.ins[0].index,
            txHex,
            makerRawTx.data.bitcoin_tx.output[makerTx.bitcoin_tx.ins[0].index].value,
            input_proof.data.receipt,
            input_proof.data.innerKey!,
          ),
        );
      }
    }

    let makerOuts: ReceiptOutput[] = [];
    makerReceival.forEach((value, tokenPubkey) => {
      makerOuts.push(
        this.createLrc20ChangeOutput(new Receipt(new TokenAmount(value.valueOf()), tokenPubkey), makerPubKey),
      );
    });

    const changeBtcOutput = this.createReceiptBtcChangeOutput();

    const btcInputs = new BtcUtxosCoinSelection(this.btcUtxos).selectUtxos(
      0,
      [...takerInputs].length,
      [...takerOutputs, ...makerOuts].length,
      feeRateVb,
    );
    const makerInputs = await this.createInputsFromUtxos([...btcInputs]);

    const tx = this.builder.buildAndSignTakerPsbt(
      psbt,
      takerInputs,
      makerInputs,
      [...makerOuts, changeBtcOutput],
      feeRateVb,
    );

    let lrc20Tx = this.convertToLrc20Transaction(
      [...takerInputs, ...makerInputs],
      [...takerOutputs, ...makerOuts, changeBtcOutput],
      tx,
      Lrc20TransactionTypeEnum.Transfer,
    );

    // Change mocked taker' witnesses to it's real one
    for (let i = 0; i < takerTxs.length; i++) {
      tx.ins[i].witness = takerTxs[i].bitcoin_tx.ins[0].witness;
    }
    tx.ins[takerTxs.length].witness = makerTx.bitcoin_tx.ins[0].witness;

    return lrc20Tx;
  }

  /**
   * Verifies that received PSBT that is sent by a maker is valid
   *
   * @returns A boolean in such a cases:
   * @param transactions received PSBTs
   * @param payAmount (expected amount of tokens to pay by a maker) is less than amount in output proof, returns false.
   * @param receive (expected amount of tokens to receive by a maker filtered by tokenPubkey) is bigger than amount in input proof, also returns false.
   *
   * Also returns false in case when proofs are incorrect
   */
  public arePsbtsAsExpected(
    transactions: Lrc20Transaction[],
    payAmount: bigint,
    receive: { amount: bigint; tokenPubkey: string }[],
  ): boolean {
    let inputTokens: Map<string, bigint> = new Map();
    let outputTokens = 0n;
    for (let i = 0; i < transactions.length; i++) {
      if (transactions[i].tx_type.type == Lrc20TransactionTypeEnum.Transfer) {
        const input_proof = (transactions[i].tx_type.data as TransferData).input_proofs.get(0)!;
        if (input_proof.type === ReceiptProofType.Sig) {
          const tweakedPubKey = Receipt.receiptPublicKey(
            Buffer.from(input_proof.data.innerKey, "hex"),
            input_proof.data.receipt,
          ).toString("hex");
          if (tweakedPubKey !== transactions[i].bitcoin_tx.ins[0].witness[1].toString("hex")) {
            return false;
          }

          let tokensAmount = inputTokens.get(input_proof.data.receipt.tokenPubkey.inner.toString("hex"));
          if (tokensAmount === undefined) {
            tokensAmount = 0n;
          }
          inputTokens.set(
            input_proof.data.receipt.tokenPubkey.inner.toString("hex"),
            tokensAmount + input_proof.data.receipt.tokenAmount.amount,
          );
        }

        const output_proof = (transactions[i].tx_type.data as TransferData).output_proofs.get(0)!;
        if ((output_proof.type = ReceiptProofType.Sig)) {
          const tweakedPubKey = Receipt.receiptPublicKey(
            Buffer.from((output_proof.data as SigReceiptProofData).innerKey, "hex"),
            output_proof.data.receipt,
          );
          if (!this.pubkeyInScript(tweakedPubKey, transactions[i].bitcoin_tx.outs[0].script)) {
            return false;
          }
        }

        outputTokens += output_proof.data.receipt.tokenAmount.amount;
      }
    }

    if (inputTokens.size != receive.length) {
      return false;
    }

    for (let i = 0; i < receive.length; i++) {
      let inputAmount = inputTokens.get(receive[i].tokenPubkey);
      if (inputAmount < receive[i].amount) {
        return false;
      }
    }

    return outputTokens <= payAmount;
  }

  public async getLrc20Balances(): Promise<{ tokenPubkey: string; balance: bigint; name: string; symbol: string }[]> {
    const balances: {
      tokenPubkey: string;
      balance: bigint;
      name: string;
      symbol: string;
    }[] = [];
    const tokenPubkeyMap = new Map<string, { balance: bigint; name: string; symbol: string }>();
    const tokenPubkeyHexesToFetch = new Set<string>();

    for (const utxo of this.lrc20Utxos) {
      const { tokenPubkey, tokenAmount } = getReceiptDataFromProof(utxo.receipt)!.receipt!;
      const tokenPubkeyHex = tokenPubkey.inner.toString("hex");

      if (!this.tokenInfoMap.has(tokenPubkeyHex)) {
        tokenPubkeyHexesToFetch.add(tokenPubkeyHex);
      }

      if (tokenPubkeyMap.has(tokenPubkeyHex)) {
        const existing = tokenPubkeyMap.get(tokenPubkeyHex)!;
        tokenPubkeyMap.set(tokenPubkeyHex, {
          balance: existing.balance + tokenAmount.amount,
          name: "",
          symbol: "",
        });
      } else {
        tokenPubkeyMap.set(tokenPubkeyHex, {
          balance: tokenAmount.amount,
          name: "",
          symbol: "",
        });
      }
    }

    // Fetch all tokenPubkey info in parallel
    const tokenPubkeyInfoPromises = Array.from(tokenPubkeyHexesToFetch).map((tokenPubkeyHex) =>
      this.getTokenPubkeyInfo(tokenPubkeyHex),
    );
    const tokenPubkeyInfos = await Promise.all(tokenPubkeyInfoPromises);

    // Update tokenInfoMap with fetched tokenPubkey info
    tokenPubkeyInfos.forEach((tokenPubkeyInfo, index) => {
      const tokenPubkeyHex = Array.from(tokenPubkeyHexesToFetch)[index];
      this.tokenInfoMap.set(tokenPubkeyHex, tokenPubkeyInfo);
    });

    // Populate the balances array with the fetched tokenPubkey info
    tokenPubkeyMap.forEach((value, tokenPubkeyHex) => {
      const tokenPubkeyInfo = this.tokenInfoMap.get(tokenPubkeyHex)!;
      const name = tokenPubkeyInfo.announcement?.name || `${tokenPubkeyHex.slice(0, 3)}...${tokenPubkeyHex.slice(-3)}`;
      const symbol = tokenPubkeyInfo.announcement?.symbol || `${tokenPubkeyHex.slice(0, 3)}`;
      balances.push({
        tokenPubkey: tokenPubkeyHex,
        balance: value.balance,
        name,
        symbol,
      });
    });

    return balances;
  }

  public async toBtcMetadata(tx: Lrc20Transaction): Promise<BtcMetadata> {
    const txid = tx.bitcoin_tx.getId();

    // Collect UTXOs being spent and calculate total input value
    const utxosSpent = {
      btc: [] as Array<{ txid: string; vout: number; value: number }>,
      lrc20: [] as Array<{ txid: string; vout: number; value: number }>,
    };

    const inputValue = await Promise.all(
      tx.bitcoin_tx.ins.map(async (input, index) => {
        const prevTxid = input.hash.reverse().toString("hex");
        const prevVout = input.index;
        const utxoValue = await this.electrsApi.getUtxoValue(prevTxid, prevVout);

        const utxoData = {
          txid: prevTxid,
          vout: prevVout,
          value: utxoValue,
        };

        if (tx.tx_type.type === Lrc20TransactionTypeEnum.Transfer) {
          const transferData = tx.tx_type.data as TransferData;
          if (transferData.input_proofs.has(index)) {
            utxosSpent.lrc20.push(utxoData);
          } else {
            utxosSpent.btc.push(utxoData);
          }
        } else {
          utxosSpent.btc.push(utxoData);
        }

        return utxoValue;
      }),
    ).then((values) => values.reduce((sum, value) => sum + value, 0));

    // Calculate fees paid
    const outputValue = tx.bitcoin_tx.outs.reduce((sum, output) => sum + output.value, 0);
    const feesPaid = inputValue - outputValue;

    return {
      txid,
      feesPaid,
      utxosSpent,
    };
  }

  private convertToLrc20Transaction(
    inputs: Array<TxInput>,
    outputs: Array<TxOutput>,
    transaction: Transaction,
    type: Lrc20TransactionTypeEnum,
    announcement?: AnnouncementData,
  ): Lrc20Transaction {
    const outputStartIndex =
      type === Lrc20TransactionTypeEnum.Announcement || type === Lrc20TransactionTypeEnum.Issue ? 1 : 0;
    const inputProofs = this.createInputProofs(inputs);
    const outputProofs = this.createOutputProofs(outputs, outputStartIndex);

    let txType: Lrc20TransactionType;

    switch (type) {
      case Lrc20TransactionTypeEnum.Announcement:
        if (!announcement) {
          throw new Error("Announcement data is not provided");
        }
        txType = {
          type: Lrc20TransactionTypeEnum.Announcement,
          data: announcement,
        };
        break;
      case Lrc20TransactionTypeEnum.Transfer:
        txType = {
          type: Lrc20TransactionTypeEnum.Transfer,
          data: {
            input_proofs: inputProofs,
            output_proofs: outputProofs,
          },
        };
        break;
      case Lrc20TransactionTypeEnum.Issue:
        const announcementInfo = {
          tokenPubkey: (outputs[1] as ReceiptOutput).receipt.tokenPubkey.inner.toString("hex"),
          amount: outputs
            .filter((output) => output instanceof ReceiptOutput || output instanceof MultisigReceiptOutput)
            .reduce((acc, output) => {
              return acc + (output as ReceiptOutput).receipt.tokenAmount.amount;
            }, 0n),
        };
        txType = {
          type: Lrc20TransactionTypeEnum.Issue,
          data: {
            announcement: announcementInfo,
            input_proofs: inputProofs,
            output_proofs: outputProofs,
          },
        };
        break;
      case Lrc20TransactionTypeEnum.SparkExit:
        txType = {
          type: Lrc20TransactionTypeEnum.SparkExit,
          data: {
            output_proofs: outputProofs,
          },
        };
        break;
      default:
        throw new Error("Unsupported transaction type");
    }

    return new Lrc20Transaction(transaction, txType);
  }

  private createLrc20ChangeOutputs(inputs: Array<TxInput>, outputs: Array<TxOutput>): Array<ReceiptOutput> {
    const amountsMap = new Map<string, { inputsSum: bigint; outputsSum: bigint }>();

    inputs.forEach((input) => {
      if (input instanceof ReceiptInput || input instanceof MultisigReceiptInput) {
        const tokenPubkey = input.proof.tokenPubkey.inner.toString("hex");
        if (amountsMap.get(tokenPubkey)) {
          const data = amountsMap.get(tokenPubkey)!;
          data.inputsSum += input.proof.tokenAmount.amount;
          amountsMap.set(tokenPubkey, data);
          return;
        }
        amountsMap.set(tokenPubkey, {
          inputsSum: input.proof.tokenAmount.amount,
          outputsSum: BigInt(0),
        });
      }
    });

    outputs.forEach((output) => {
      if (
        (output instanceof ReceiptOutput || output instanceof MultisigReceiptOutput) &&
        !output.receipt.isEmptyReceipt()
      ) {
        const tokenPubkey = output.receipt.tokenPubkey.inner.toString("hex");
        const data = amountsMap.get(tokenPubkey)!;
        data.outputsSum += output.receipt.tokenAmount.amount;
      }
    });

    let changeOutputs = new Array<ReceiptOutput>();
    amountsMap.forEach((value, key) => {
      const changeAmount = value.inputsSum - value.outputsSum;
      if (changeAmount > 0) {
        changeOutputs.push(
          this.createLrc20ChangeOutput(
            new Receipt(new TokenAmount(changeAmount), new TokenPubkey(Buffer.from(key, "hex"))),
          ),
        );
      }
    });

    return changeOutputs;
  }

  private createLrc20ChangeOutput(
    changeReceipt: Receipt,
    receiverPubKey: Buffer = this.addressInnerKey,
  ): ReceiptOutput {
    return ReceiptOutput.createFromRaw(receiverPubKey, 1000, changeReceipt);
  }

  private createInputProofs(inputs: TxInput[]): Map<number, ReceiptProof> {
    const inputProofs = new Map<number, ReceiptProof>();
    inputs.forEach((input, index) => {
      if (input instanceof ReceiptInput) {
        inputProofs.set(index, input.toReceiptProofs());
      }
      if (input instanceof MultisigReceiptInput) {
        inputProofs.set(index, input.toReceiptProofs());
      }
    });
    return inputProofs;
  }

  public async broadcast(transaction: Lrc20TransactionDto | string): Promise<string> {
    try {
      if (typeof transaction === "string") {
        transaction = JSONParse(transaction);
        transaction = plainToInstance(Lrc20TransactionDto, transaction);
      }
      let tx = Lrc20Transaction.fromLrc20TransactionDto(transaction);
      let isBroadcasted = false;
      if (
        transaction.tx_type.type === Lrc20TransactionTypeEnum.Transfer ||
        transaction.tx_type.type === Lrc20TransactionTypeEnum.SparkExit
      ) {
        isBroadcasted = await this.lrcNodeApi.sendRawLrc20Tx(tx);
      } else if (
        transaction.tx_type.type === Lrc20TransactionTypeEnum.Announcement ||
        transaction.tx_type.type === Lrc20TransactionTypeEnum.Issue
      ) {
        isBroadcasted = await this.lrcNodeApi.sendRawLrc20Tx(tx, tx.bitcoin_tx.outs[0].value);
      } else {
        throw new Error("Unsupported transaction type");
      }

      // if (isBroadcasted) {
      //   this.spentUtxo(tx);
      // }

      return isBroadcasted ? tx.bitcoin_tx.getId() : "";
    } catch (e) {
      console.log("error: ", e);
    }
  }

  public broadcastRawBtcTransaction(txHex: string): Promise<string> {
    return this.electrsApi.sendTransaction(txHex);
  }

  public spentUtxo(tx: Lrc20Transaction) {
    const btcInputs = tx.bitcoin_tx.ins;
    this.btcUtxos = this.btcUtxos.filter((utxo) => {
      return (
        btcInputs.filter((input) => {
          const isEqual = reverseBuffer(input.hash).toString("hex") === utxo.txid && BigInt(input.index) == utxo.vout;
          if (isEqual) {
            let newUtxos = filterUniqueUtxo(this.spentBtcUtxos, [utxo]);
            this.spentBtcUtxos = [...this.spentBtcUtxos, ...newUtxos];
          }

          return isEqual;
        }).length == 0
      );
    });

    let lrc20Inputs: Map<number, ReceiptProof>;
    switch (tx.tx_type.type) {
      case Lrc20TransactionTypeEnum.Announcement: {
        return;
      }
      case Lrc20TransactionTypeEnum.Transfer:
      case Lrc20TransactionTypeEnum.Issue: {
        lrc20Inputs = tx.tx_type.data.input_proofs;
      }
    }

    this.lrc20Utxos = this.lrc20Utxos.filter((utxo) => {
      for (let [index, lrc20Input] of lrc20Inputs.entries()) {
        const input = btcInputs[index];

        if (reverseBuffer(input.hash).toString("hex") === utxo.txid && BigInt(input.index) === utxo.vout) {
          let newUtxos = filterUniqueUtxo(this.spentLrc20Utxos, [utxo]);
          this.spentLrc20Utxos = [...this.spentLrc20Utxos, ...newUtxos];

          return false;
        }
      }

      return true;
    });
  }

  private createReceiptBtcChangeOutput(): ReceiptOutput {
    return ReceiptOutput.createFromRaw(this.addressInnerKey, 1000, new Receipt(new TokenAmount(0n), new TokenPubkey()));
  }

  private createRawBtcChangeOutput(): BitcoinOutput {
    return new BitcoinOutput(this.keyPair.publicKey, 1000);
  }

  private createOutputProofs(outputs: TxOutput[], index = 0): Map<number, ReceiptProof> {
    const outputProofs = new Map<number, ReceiptProof>();
    outputs.forEach((output) => {
      if (output instanceof ReceiptOutput) {
        outputProofs.set(index++, (output as ReceiptOutput).toReceiptProof());
      }
      if (output instanceof SparkExitOutput) {
        outputProofs.set(index++, (output as SparkExitOutput).toReceiptProof());
      }
      if (output instanceof MultisigReceiptOutput) {
        outputProofs.set(index++, (output as MultisigReceiptOutput).toReceiptProof(this.network));
      }
    });

    return outputProofs;
  }

  public async createInputsFromUtxos(utxos: Array<BitcoinUtxo | Lrc20Utxo>): Promise<TxInput[]> {
    return Promise.all(
      utxos.map(async (utxo) => {
        return BitcoinInput.createFromRaw(
          utxo.txid,
          Number(utxo.vout),
          await this.electrsApi.getTransactionHex(utxo.txid),
          utxo.satoshis,
        );
      }),
    );
  }

  public createOutputs(
    amounts: Array<{
      recipient?: string | Array<string>;
      amount: bigint;
      tokenPubkey: string;
      sats?: number;
      m?: number;
      cltvOutputLocktime?: number;
      revocationKey?: string;
      expiryKey?: string;
      metadata?: SparkExitMetadata;
    }>,
  ): Array<TxOutput> {
    if (amounts.filter((amount) => amount.metadata).length > 1) {
      throw new Error("Only 1 LTTXO can be withdrawn in 1 transaction");
    }

    return amounts.map((token) => {
      const tokenPubkeyBuf = Buffer.from(token.tokenPubkey, "hex");

      const receipt =
        tokenPubkeyBuf == EMPTY_TOKEN_PUBKEY
          ? Receipt.emptyReceipt()
          : new Receipt(new TokenAmount(token.amount), new TokenPubkey(tokenPubkeyBuf));

      if (token.metadata) {
        return SparkExitOutput.createFromRaw(
          Buffer.from(token.revocationKey, "hex"),
          Buffer.from(token.expiryKey, "hex"),
          token.cltvOutputLocktime,
          token.sats,
          receipt,
          token.metadata,
        );
      } else if (typeof token.recipient === "string") {
        const addressParsed = address.fromBech32(token.recipient);
        return ReceiptOutput.createFromRaw(toEvenParity(addressParsed.data), token.sats || 1000, receipt);
      } else if (Array.isArray(token.recipient)) {
        const expiryPublicKey = token.expiryKey ? token.expiryKey : token.recipient[0];
        const expiryReceiptKey = Receipt.receiptPublicKey(address.fromBech32(expiryPublicKey).data, receipt);

        return MultisigReceiptOutput.createFromRaw(
          token.recipient.map((addr) => {
            const addressParsed = address.fromBech32(addr);
            return toEvenParity(addressParsed.data);
          }),
          token.m || 2,
          token.sats || 1000,
          receipt,
          token.cltvOutputLocktime,
          expiryReceiptKey,
        );
      }
    });
  }

  private pubkeyPositionInScript(pubkey: Buffer, outScript: Buffer) {
    const pubkeyHash = crypto.hash160(pubkey);
    const pubkeyXOnly = pubkey.slice(1, 33);
    const decompiled = script.decompile(outScript);
    if (decompiled === null) throw new Error("Unknown script error");
    return decompiled.findIndex((element) => {
      if (typeof element === "number") return false;
      return element.equals(pubkey) || element.equals(pubkeyHash) || element.equals(pubkeyXOnly);
    });
  }

  public pubkeyInScript(pubkey: Buffer, outScript: Buffer) {
    return this.pubkeyPositionInScript(pubkey, outScript) !== -1;
  }

  public hasEmptyReceiptProof(utxo: Lrc20Utxo | BitcoinUtxo) {
    // Check if it's an EmptyReceipt
    if (utxo instanceof Lrc20Utxo) {
      if (!utxo.receipt || utxo.receipt.type === ReceiptProofType.EmptyReceipt) {
        return true;
      }

      if (utxo.receipt.type === ReceiptProofType.Sig) {
        const emptyTokenPubkeyBytes = new Uint8Array(32).fill(0x02);
        const emptyTokenPubkey = Buffer.from(emptyTokenPubkeyBytes).toString("hex");
        return utxo.receipt.data.receipt.tokenPubkey.pubkey.toString("hex") === emptyTokenPubkey;
      }
    }

    return true;
  }

  public selectUtxo(amount: bigint, tokenPubkey?: string): Lrc20Utxo | BitcoinUtxo {
    let selectedUtxo: BitcoinUtxo | Lrc20Utxo | undefined;

    if (tokenPubkey) {
      // Select Lrc20Utxo
      selectedUtxo = this.lrc20Utxos.find(
        (utxo) =>
          utxo.receipt.data.receipt.tokenPubkey.inner.toString("hex") === tokenPubkey &&
          utxo.receipt.data.receipt.tokenAmount.amount >= amount,
      );
    } else {
      // Select BitcoinUtxo
      selectedUtxo = this.btcUtxos.find((utxo) => utxo.satoshis >= amount);
    }

    if (!selectedUtxo) {
      throw new Error("No suitable UTXO found");
    }

    return selectedUtxo;
  }

  public async coinselect(tokenPubkey: string, amount: bigint, feeRate = 1.0): Promise<Array<BitcoinUtxo | Lrc20Utxo>> {
    if (tokenPubkey.length > 0) {
      const tokens = [{ tokenPubkey, amount }];
      const selectedLrc20Utxos = new Lrc20UtxosCoinSelection(this.lrc20Utxos).selectUtxos(tokens);
      const changeBtcOutput = this.createReceiptBtcChangeOutput();
      const lrc20Inputs = await this.createInputsFromUtxos(selectedLrc20Utxos); // Convert Lrc20Utxo to TxInput
      const changeLrc20Outputs = this.createLrc20ChangeOutputs(lrc20Inputs, []);
      // const btcInputs = new BtcUtxosCoinSelection(this.btcUtxos).selectUtxos(
      //   0,
      //   [...lrc20Inputs].length,
      //   [...changeLrc20Outputs, changeBtcOutput].length,
      //   1, // Assuming feeRateVb is 1 for this example
      // );
      // return [...selectedLrc20Utxos, ...btcInputs];
      return [...selectedLrc20Utxos];
    } else {
      return new BtcUtxosCoinSelection(this.btcUtxos).selectUtxos(
        0,
        1,
        1,
        feeRate,
        false,
        amount + BigInt(DUST_AMOUNT * feeRate),
      );
    }
  }

  public async sendPostRequest(url: string, data: any): Promise<Response> {
    try {
      const response = await fetch(url, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
        },
        body: JSONStringifyBodyDown(data),
      });

      if (!response.ok) {
        // response.json();
        console.log(`Response not OK: ${await response.text()}`);
        console.log(`Response not OK: ${JSON.stringify(response)}`);
      }
      return response;
    } catch (error) {
      console.log(`Request data: ${JSONStringifyBodyDown(data)}`);
      throw error;
    }
  }

  public async sendGetRequest(url: string, data?: unknown): Promise<Response> {
    return await fetch(url, {
      method: "GET",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify(data),
    });
  }

  public async signRawTransaction(hex: string): Promise<Transaction> {
    let unsignedTx = Transaction.fromHex(hex);
    let prevouts = new Map<String, String>();

    await Promise.all(
      unsignedTx.ins.map(async (input) => {
        let hash = input.hash.toString("hex");
        let txid = reverseBuffer(input.hash).toString("hex");
        let hex = await this.electrsApi.getTransactionHex(txid);

        prevouts.set(hash, hex);
      }),
    );

    return this.builder.signRawTransaction(unsignedTx, prevouts);
  }
}
