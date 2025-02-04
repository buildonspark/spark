import { secp256k1 } from "@noble/curves/secp256k1";
import { Address, OutScript, Transaction } from "@scure/btc-signer";
import {
  AddressNode,
  AddressRequestNode,
  CreateTreeRequest,
  CreateTreeResponse,
  CreationNode,
  CreationResponseNode,
  FinalizeNodeSignaturesResponse,
  NodeSignatures,
  PrepareTreeAddressRequest,
  PrepareTreeAddressResponse,
  SigningJob,
  TreeNode,
} from "../proto/spark";
import {
  getP2TRAddressFromPublicKey,
  getSigHashFromTx,
  getTxFromRawTxBytes,
  getTxId,
} from "../utils/bitcoin";
import { subtractPrivateKeys } from "../utils/keys";
import { getNetwork, Network } from "../utils/network";
import {
  copySigningCommitment,
  getRandomSigningNonce,
  getSigningCommitmentFromNonce,
} from "../utils/signing";
import { aggregateFrost, signFrost } from "../utils/wasm";
import { KeyPackage, SigningNonce } from "../wasm/spark_bindings";
import { WalletConfigService } from "./config";
import { ConnectionManager } from "./connection";

export type DepositAddressTree = {
  address?: string | undefined;
  signingPrivateKey: Uint8Array;
  verificationKey?: Uint8Array | undefined;
  children: DepositAddressTree[];
};

export type CreationNodeWithNonces = CreationNode & {
  nodeTxSigningNonce?: SigningNonce | undefined;
  refundTxSigningNonce?: SigningNonce | undefined;
};

const INITIAL_TIME_LOCK = 200;

export class TreeCreationService {
  private readonly config: WalletConfigService;
  private readonly connectionManager: ConnectionManager;

  constructor(
    config: WalletConfigService,
    connectionManager: ConnectionManager
  ) {
    this.config = config;
    this.connectionManager = connectionManager;
  }

  async generateDepositAddressForTree(
    vout: number,
    parentSigningPrivKey: Uint8Array,
    parentTx?: Transaction,
    parentNode?: TreeNode
  ): Promise<DepositAddressTree> {
    const tree = this.createDepositAddressTree(parentSigningPrivKey);
    const addressRequestNodes =
      this.createAddressRequestNodeFromTreeNodes(tree);
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
      this.config
    );

    const request: PrepareTreeAddressRequest = {
      userIdentityPublicKey: this.config.getIdentityPublicKey(),
      node: undefined,
    };
    if (parentNode) {
      if (!parentNode.parentNodeId) {
        throw new Error("Parent node ID is undefined");
      }
      request.parentNodeOutput = {
        nodeId: parentNode.parentNodeId,
        vout: vout,
      };
    } else if (parentTx) {
      request.onChainUtxo = {
        txid: getTxId(parentTx),
        vout: vout,
        rawTx: parentTx.toBytes(),
      };
    } else {
      throw new Error("No parent node or parent tx provided");
    }

    const pubkey = secp256k1.getPublicKey(parentSigningPrivKey, true);
    request.node = {
      userPublicKey: pubkey,
      children: addressRequestNodes,
    };

    const root: DepositAddressTree = {
      address: undefined,
      signingPrivateKey: parentSigningPrivKey,
      children: tree,
    };

    let response: PrepareTreeAddressResponse;
    try {
      response = await sparkClient.prepare_tree_address(request);
    } catch (error) {
      throw new Error(`Error preparing tree address: ${error}`);
    } finally {
      sparkClient.close?.();
    }

    if (!response.node) {
      throw new Error("No node found in response");
    }

    this.applyAddressNodesToTree([root], [response.node]);

    return root;
  }

  async createTree(
    vout: number,
    root: DepositAddressTree,
    createLeaves: boolean,
    parentTx?: Transaction,
    parentNode?: TreeNode
  ): Promise<FinalizeNodeSignaturesResponse> {
    const request: CreateTreeRequest = {
      userIdentityPublicKey: this.config.getIdentityPublicKey(),
      node: undefined,
    };

    let tx: Transaction | undefined;
    if (parentTx) {
      tx = parentTx;
      request.onChainUtxo = {
        txid: getTxId(parentTx),
        vout: vout,
        rawTx: parentTx.toBytes(),
      };
    } else if (parentNode) {
      tx = getTxFromRawTxBytes(parentNode.nodeTx);
      if (!parentNode.parentNodeId) {
        throw new Error("Parent node ID is undefined");
      }
      request.parentNodeOutput = {
        nodeId: parentNode.parentNodeId,
        vout: vout,
      };
    } else {
      throw new Error("No parent node or parent tx provided");
    }

    const rootCreationNode = this.buildCreationNodesFromTree(
      vout,
      createLeaves,
      this.config.getConfig().network,
      root,
      tx
    );

    request.node = rootCreationNode;

    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
      this.config
    );

    let response: CreateTreeResponse;
    try {
      response = await sparkClient.create_tree(request);
    } catch (error) {
      sparkClient.close?.();
      throw new Error(`Error creating tree: ${error}`);
    }

    if (!response.node) {
      throw new Error("No node found in response");
    }

    const creationResultTreeRoot = response.node;

    const nodeSignatures = await this.signTreeCreation(
      tx,
      vout,
      root,
      rootCreationNode,
      creationResultTreeRoot
    );

    let finalizeResp: FinalizeNodeSignaturesResponse;
    try {
      finalizeResp = await sparkClient.finalize_node_signatures({
        nodeSignatures: nodeSignatures,
      });
    } catch (error) {
      throw new Error(
        `Error finalizing node signatures in tree creation: ${error}`
      );
    } finally {
      sparkClient.close?.();
    }

    return finalizeResp;
  }

  private createDepositAddressTree(
    targetSigningPrivateKey: Uint8Array
  ): DepositAddressTree[] {
    const leftKey = secp256k1.utils.randomPrivateKey();
    const leftNode: DepositAddressTree = {
      signingPrivateKey: leftKey,
      children: [],
    };

    const rightKey = subtractPrivateKeys(targetSigningPrivateKey, leftKey);

    const rightNode: DepositAddressTree = {
      signingPrivateKey: rightKey,
      children: [],
    };
    return [leftNode, rightNode];
  }

  private createAddressRequestNodeFromTreeNodes(
    treeNodes: DepositAddressTree[]
  ): AddressRequestNode[] {
    const results = [];
    for (const node of treeNodes) {
      const pubkey = secp256k1.getPublicKey(node.signingPrivateKey, true);
      const result: AddressRequestNode = {
        userPublicKey: pubkey,
        children: this.createAddressRequestNodeFromTreeNodes(node.children),
      };
      results.push(result);
    }
    return results;
  }

  private applyAddressNodesToTree(
    tree: DepositAddressTree[],
    addressNodes: AddressNode[]
  ) {
    for (let i = 0; i < tree.length; i++) {
      tree[i].address = addressNodes[i].address?.address;
      tree[i].verificationKey = addressNodes[i].address?.verifyingKey;
      this.applyAddressNodesToTree(tree[i].children, addressNodes[i].children);
    }
  }

  private buildChildCreationNode(
    node: DepositAddressTree,
    parentTx: Transaction,
    vout: number,
    network: Network
  ): CreationNodeWithNonces {
    // internal node
    const internalCreationNode: CreationNodeWithNonces = {
      nodeTxSigningJob: undefined,
      refundTxSigningJob: undefined,
      children: [],
    };

    const tx = new Transaction();
    tx.addInput({
      txid: getTxId(parentTx),
      index: vout,
    });

    const parentTxOut = parentTx.getOutput(vout);
    if (!parentTxOut?.script || !parentTxOut?.amount) {
      throw new Error("parentTxOut is undefined");
    }

    tx.addOutput({
      script: parentTxOut.script,
      amount: parentTxOut.amount,
    });

    tx.updateInput(0, {
      finalScriptSig: parentTxOut.script,
    });

    const pubkey = secp256k1.getPublicKey(node.signingPrivateKey, true);
    const signingNonce = getRandomSigningNonce();
    const signingNonceCommitment = getSigningCommitmentFromNonce(signingNonce);
    const signingJob: SigningJob = {
      signingPublicKey: pubkey,
      rawTx: tx.toBytes(),
      signingNonceCommitment: signingNonceCommitment,
    };

    internalCreationNode.nodeTxSigningNonce = signingNonce;
    internalCreationNode.nodeTxSigningJob = signingJob;

    // leaf node
    const sequence = 1 << 30 || INITIAL_TIME_LOCK;

    const childCreationNode: CreationNodeWithNonces = {
      nodeTxSigningJob: undefined,
      refundTxSigningJob: undefined,
      children: [],
    };

    const childTx = new Transaction();
    childTx.addInput({
      txid: getTxId(parentTx),
      index: 0,
      sequence,
    });

    childTx.addOutput({
      script: parentTxOut.script,
      amount: parentTxOut.amount,
    });

    const childPubkey = secp256k1.getPublicKey(node.signingPrivateKey, true);
    const childSigningNonce = getRandomSigningNonce();
    const childSigningNonceCommitment =
      getSigningCommitmentFromNonce(childSigningNonce);
    const childSigningJob: SigningJob = {
      signingPublicKey: childPubkey,
      rawTx: childTx.toBytes(),
      signingNonceCommitment: childSigningNonceCommitment,
    };

    childCreationNode.nodeTxSigningNonce = childSigningNonce;
    childCreationNode.nodeTxSigningJob = childSigningJob;

    const refundTx = new Transaction();
    refundTx.addInput({
      txid: tx.id,
      index: 0,
      sequence,
    });

    const refundP2trAddress = getP2TRAddressFromPublicKey(pubkey, network);
    const refundAddress = Address(getNetwork(network)).decode(
      refundP2trAddress
    );
    const refundPkScript = OutScript.encode(refundAddress);
    refundTx.addOutput({
      script: refundPkScript,
      amount: parentTxOut.amount,
    });

    refundTx.updateInput(0, {
      finalScriptSig: parentTxOut.script,
    });

    const refundSigningNonce = getRandomSigningNonce();
    const refundSigningNonceCommitment =
      getSigningCommitmentFromNonce(refundSigningNonce);

    const refundSigningJob: SigningJob = {
      signingPublicKey: pubkey,
      rawTx: refundTx.toBytes(),
      signingNonceCommitment: refundSigningNonceCommitment,
    };
    childCreationNode.refundTxSigningNonce = refundSigningNonce;
    childCreationNode.refundTxSigningJob = refundSigningJob;

    internalCreationNode.children.push(childCreationNode);

    return internalCreationNode;
  }

  private buildCreationNodesFromTree(
    vout: number,
    createLeaves: boolean,
    network: Network,
    root: DepositAddressTree,
    parentTx: Transaction
  ): CreationNodeWithNonces {
    const parentTxOutput = parentTx.getOutput(vout);
    if (!parentTxOutput?.script || !parentTxOutput?.amount) {
      throw new Error("parentTxOutput is undefined");
    }
    const rootNodeTx = new Transaction();
    rootNodeTx.addInput({
      txid: getTxId(parentTx),
      index: vout,
    });

    for (let i = 0; i < root.children.length; i++) {
      const child = root.children[i];
      if (!child.address) {
        throw new Error("child address is undefined");
      }
      const childAddress = Address(getNetwork(network)).decode(child.address);
      const childPkScript = OutScript.encode(childAddress);
      rootNodeTx.addOutput({
        script: childPkScript,
        amount: parentTxOutput.amount / 2n,
      });
    }

    rootNodeTx.updateInput(0, {
      finalScriptSig: parentTxOutput.script,
    });

    const rootNodeSigningNonce = getRandomSigningNonce();
    const rootNodeSigningJob: SigningJob = {
      signingPublicKey: secp256k1.getPublicKey(root.signingPrivateKey, true),
      rawTx: rootNodeTx.toBytes(),
      signingNonceCommitment:
        getSigningCommitmentFromNonce(rootNodeSigningNonce),
    };
    const rootCreationNode: CreationNodeWithNonces = {
      nodeTxSigningJob: rootNodeSigningJob,
      refundTxSigningJob: undefined,
      children: [],
    };
    rootCreationNode.nodeTxSigningNonce = rootNodeSigningNonce;

    const leftChildCreationNode = this.buildChildCreationNode(
      root.children[0],
      rootNodeTx,
      0,
      network
    );
    const rightChildCreationNode = this.buildChildCreationNode(
      root.children[1],
      rootNodeTx,
      1,
      network
    );

    rootCreationNode.children.push(leftChildCreationNode);
    rootCreationNode.children.push(rightChildCreationNode);

    return rootCreationNode;
  }

  private signNodeCreation(
    parentTx: Transaction,
    vout: number,
    internalNode: DepositAddressTree,
    creationNode: CreationNodeWithNonces,
    creationResponseNode: CreationResponseNode
  ): { tx: Transaction; signature: NodeSignatures } {
    if (
      !creationNode.nodeTxSigningJob?.signingPublicKey ||
      !internalNode.verificationKey
    ) {
      throw new Error("signingPublicKey or verificationKey is undefined");
    }

    const rootKeyPackage = new KeyPackage(
      internalNode.signingPrivateKey,
      creationNode.nodeTxSigningJob.signingPublicKey,
      internalNode.verificationKey
    );

    const parentTxOutput = parentTx.getOutput(vout);
    if (!parentTxOutput) {
      throw new Error("parentTxOutput is undefined");
    }

    const tx = getTxFromRawTxBytes(creationNode.nodeTxSigningJob.rawTx);
    const txSighash = getSigHashFromTx(tx, 0, parentTxOutput);

    let nodeTxSignature: Uint8Array = new Uint8Array();
    if (creationNode.nodeTxSigningNonce) {
      const signingNonceCommitment = getSigningCommitmentFromNonce(
        creationNode.nodeTxSigningNonce
      );

      const userSignature = signFrost({
        msg: txSighash,
        keyPackage: rootKeyPackage,
        nonce: creationNode.nodeTxSigningNonce,
        selfCommitment: copySigningCommitment(signingNonceCommitment),
        statechainCommitments:
          creationResponseNode.nodeTxSigningResult?.signingNonceCommitments,
      });

      nodeTxSignature = aggregateFrost({
        msg: txSighash,
        statechainSignatures:
          creationResponseNode.nodeTxSigningResult?.signatureShares,
        statechainPublicKeys:
          creationResponseNode.nodeTxSigningResult?.publicKeys,
        verifyingKey: internalNode.verificationKey,
        statechainCommitments:
          creationResponseNode.nodeTxSigningResult?.signingNonceCommitments,
        selfCommitment: signingNonceCommitment,
        selfSignature: userSignature,
        selfPublicKey: secp256k1.getPublicKey(
          internalNode.signingPrivateKey,
          true
        ),
      });
    }

    let refundTxSignature: Uint8Array = new Uint8Array();
    if (creationNode.refundTxSigningNonce) {
      const rawTx = creationNode.refundTxSigningJob?.rawTx;
      if (!rawTx) {
        throw new Error("rawTx is undefined");
      }
      if (!creationNode.refundTxSigningJob?.signingPublicKey) {
        throw new Error("signingPublicKey is undefined");
      }
      const refundTx = getTxFromRawTxBytes(rawTx);
      const refundTxSighash = getSigHashFromTx(refundTx, 0, parentTxOutput);

      const refundSigningNonceCommitment = getSigningCommitmentFromNonce(
        creationNode.refundTxSigningNonce
      );

      const refundKeyPackage = new KeyPackage(
        internalNode.signingPrivateKey,
        creationNode.nodeTxSigningJob.signingPublicKey,
        internalNode.verificationKey
      );

      const refundSigningResponse = signFrost({
        msg: refundTxSighash,
        keyPackage: refundKeyPackage,
        nonce: creationNode.refundTxSigningNonce,
        selfCommitment: copySigningCommitment(refundSigningNonceCommitment),
        statechainCommitments:
          creationResponseNode.refundTxSigningResult?.signingNonceCommitments,
      });

      refundTxSignature = aggregateFrost({
        msg: refundTxSighash,
        statechainSignatures:
          creationResponseNode.refundTxSigningResult?.signatureShares,
        statechainPublicKeys:
          creationResponseNode.refundTxSigningResult?.publicKeys,
        verifyingKey: internalNode.verificationKey,
        statechainCommitments:
          creationResponseNode.refundTxSigningResult?.signingNonceCommitments,
        selfCommitment: refundSigningNonceCommitment,
        selfSignature: refundSigningResponse,
        selfPublicKey: secp256k1.getPublicKey(
          internalNode.signingPrivateKey,
          true
        ),
      });
    }

    return {
      tx: tx,
      signature: {
        nodeId: creationResponseNode.nodeId,
        nodeTxSignature: nodeTxSignature,
        refundTxSignature: refundTxSignature,
      },
    };
  }

  private async signTreeCreation(
    tx: Transaction,
    vout: number,
    root: DepositAddressTree,
    rootCreationNode: CreationNodeWithNonces,
    creationResultTreeRoot: CreationResponseNode
  ): Promise<NodeSignatures[]> {
    const rootSignature = this.signNodeCreation(
      tx,
      vout,
      root,
      rootCreationNode,
      creationResultTreeRoot
    );

    const leftChildSignature = this.signNodeCreation(
      rootSignature.tx,
      0,
      root.children[0],
      rootCreationNode.children[0],
      creationResultTreeRoot.children[0]
    );

    const rightChildSignature = this.signNodeCreation(
      rootSignature.tx,
      1,
      root.children[1],
      rootCreationNode.children[1],
      creationResultTreeRoot.children[1]
    );

    const signatures = [
      rootSignature.signature,
      leftChildSignature.signature,
      rightChildSignature.signature,
    ];

    return signatures;
  }
}
