import { Address, OutScript, Transaction } from "@scure/btc-signer";
import { sha256 } from "@scure/btc-signer/utils";
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
} from "../proto/spark.js";
import { SigningCommitment } from "../signer/signer.js";
import {
  getP2TRAddressFromPublicKey,
  getSigHashFromTx,
  getTxFromRawTxBytes,
  getTxId,
} from "../utils/bitcoin.js";
import { getNetwork, Network } from "../utils/network.js";
import { WalletConfigService } from "./config.js";
import { ConnectionManager } from "./connection.js";

export type DepositAddressTree = {
  address?: string | undefined;
  signingPublicKey: Uint8Array;
  verificationKey?: Uint8Array | undefined;
  children: DepositAddressTree[];
};

export type CreationNodeWithNonces = CreationNode & {
  nodeTxSigningCommitment?: SigningCommitment | undefined;
  refundTxSigningCommitment?: SigningCommitment | undefined;
};

const INITIAL_TIME_LOCK = 2000;

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
    parentSigningPublicKey: Uint8Array,
    parentTx?: Transaction,
    parentNode?: TreeNode
  ): Promise<DepositAddressTree> {
    if (!parentTx && !parentNode) {
      throw new Error("No parent tx or parent node provided");
    }

    const id = parentNode?.id ?? getTxId(parentTx!);

    const tree = await this.createDepositAddressTree(
      parentSigningPublicKey,
      id
    );

    const addressRequestNodes =
      this.createAddressRequestNodeFromTreeNodes(tree);
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress()
    );

    const request: PrepareTreeAddressRequest = {
      userIdentityPublicKey: await this.config.signer.getIdentityPublicKey(),
      node: undefined,
    };
    if (parentNode) {
      if (!parentNode.parentNodeId) {
        throw new Error("Parent node ID is undefined");
      }
      request.source = {
        $case: "parentNodeOutput",
        parentNodeOutput: {
          nodeId: parentNode.parentNodeId,
          vout: vout,
        },
      };
    } else if (parentTx) {
      request.source = {
        $case: "onChainUtxo",
        onChainUtxo: {
          vout: vout,
          rawTx: parentTx.toBytes(),
          network: this.config.getNetworkProto(),
        },
      };
    } else {
      throw new Error("No parent node or parent tx provided");
    }

    request.node = {
      userPublicKey: parentSigningPublicKey,
      children: addressRequestNodes,
    };

    const root: DepositAddressTree = {
      address: undefined,
      signingPublicKey: parentSigningPublicKey,
      children: tree,
    };

    let response: PrepareTreeAddressResponse;
    try {
      response = await sparkClient.prepare_tree_address(request);
    } catch (error) {
      throw new Error(`Error preparing tree address: ${error}`);
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
      userIdentityPublicKey: await this.config.signer.getIdentityPublicKey(),
      node: undefined,
    };

    let tx: Transaction | undefined;
    if (parentTx) {
      tx = parentTx;
      request.source = {
        $case: "onChainUtxo",
        onChainUtxo: {
          vout: vout,
          rawTx: parentTx.toBytes(),
          network: this.config.getNetworkProto(),
        },
      };
    } else if (parentNode) {
      tx = getTxFromRawTxBytes(parentNode.nodeTx);
      if (!parentNode.parentNodeId) {
        throw new Error("Parent node ID is undefined");
      }
      request.source = {
        $case: "parentNodeOutput",
        parentNodeOutput: {
          nodeId: parentNode.parentNodeId,
          vout: vout,
        },
      };
    } else {
      throw new Error("No parent node or parent tx provided");
    }

    const rootCreationNode = await this.buildCreationNodesFromTree(
      vout,
      createLeaves,
      this.config.getNetwork(),
      root,
      tx
    );

    request.node = rootCreationNode;

    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress()
    );

    let response: CreateTreeResponse;
    try {
      response = await sparkClient.create_tree(request);
    } catch (error) {
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
    }

    return finalizeResp;
  }

  private async createDepositAddressTree(
    targetSigningPublicKey: Uint8Array,
    nodeId: string
  ): Promise<DepositAddressTree[]> {
    const leftKey = await this.config.signer.generatePublicKey(sha256(nodeId));
    const leftNode: DepositAddressTree = {
      signingPublicKey: leftKey,
      children: [],
    };

    const rightKey =
      await this.config.signer.subtractPrivateKeysGivenPublicKeys(
        targetSigningPublicKey,
        leftKey
      );

    const rightNode: DepositAddressTree = {
      signingPublicKey: rightKey,
      children: [],
    };
    return [leftNode, rightNode];
  }

  private createAddressRequestNodeFromTreeNodes(
    treeNodes: DepositAddressTree[]
  ): AddressRequestNode[] {
    const results: AddressRequestNode[] = [];
    for (const node of treeNodes) {
      const result: AddressRequestNode = {
        userPublicKey: node.signingPublicKey,
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
      if (!tree[i]) {
        throw new Error("Tree or address node is undefined");
      }
      if (!addressNodes[i]) {
        throw new Error("Address node is undefined");
      }
      // @ts-ignore
      tree[i].address = addressNodes[i].address?.address;
      // @ts-ignore
      tree[i].verificationKey = addressNodes[i].address?.verifyingKey;
      // @ts-ignore
      this.applyAddressNodesToTree(tree[i].children, addressNodes[i].children);
    }
  }

  private async buildChildCreationNode(
    node: DepositAddressTree,
    parentTx: Transaction,
    vout: number,
    network: Network
  ): Promise<CreationNodeWithNonces> {
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

    // Add ephemeral anchor
    tx.addOutput(this.ephemeralAnchorOutput());

    const signingNonceCommitment =
      await this.config.signer.getRandomSigningCommitment();
    const signingJob: SigningJob = {
      signingPublicKey: node.signingPublicKey,
      rawTx: tx.toBytes(),
      signingNonceCommitment: signingNonceCommitment,
    };

    internalCreationNode.nodeTxSigningCommitment = signingNonceCommitment;
    internalCreationNode.nodeTxSigningJob = signingJob;

    // leaf node
    const sequence = (1 << 30) | INITIAL_TIME_LOCK;

    const childCreationNode: CreationNodeWithNonces = {
      nodeTxSigningJob: undefined,
      refundTxSigningJob: undefined,
      children: [],
    };

    const childTx = new Transaction();
    childTx.addInput({
      txid: getTxId(tx),
      index: 0,
      sequence,
    });

    childTx.addOutput({
      script: parentTxOut.script,
      amount: parentTxOut.amount,
    });

    // Add ephemeral anchor
    childTx.addOutput(this.ephemeralAnchorOutput());

    const childSigningNonceCommitment =
      await this.config.signer.getRandomSigningCommitment();
    const childSigningJob: SigningJob = {
      signingPublicKey: node.signingPublicKey,
      rawTx: childTx.toBytes(),
      signingNonceCommitment: childSigningNonceCommitment,
    };

    childCreationNode.nodeTxSigningCommitment = childSigningNonceCommitment;
    childCreationNode.nodeTxSigningJob = childSigningJob;

    const refundTx = new Transaction();
    refundTx.addInput({
      txid: getTxId(childTx),
      index: 0,
      sequence,
    });

    const refundP2trAddress = getP2TRAddressFromPublicKey(
      node.signingPublicKey,
      network
    );
    const refundAddress = Address(getNetwork(network)).decode(
      refundP2trAddress
    );
    const refundPkScript = OutScript.encode(refundAddress);
    refundTx.addOutput({
      script: refundPkScript,
      amount: parentTxOut.amount,
    });

    const refundSigningNonceCommitment =
      await this.config.signer.getRandomSigningCommitment();

    const refundSigningJob: SigningJob = {
      signingPublicKey: node.signingPublicKey,
      rawTx: refundTx.toBytes(),
      signingNonceCommitment: refundSigningNonceCommitment,
    };
    childCreationNode.refundTxSigningCommitment = refundSigningNonceCommitment;
    childCreationNode.refundTxSigningJob = refundSigningJob;

    internalCreationNode.children.push(childCreationNode);

    return internalCreationNode;
  }

  private ephemeralAnchorOutput(): { script: Uint8Array; amount: bigint } {
    return {
      script: new Uint8Array([0x51]), // OP_TRUE
      amount: 0n,
    };
  }

  private async buildCreationNodesFromTree(
    vout: number,
    createLeaves: boolean,
    network: Network,
    root: DepositAddressTree,
    parentTx: Transaction
  ): Promise<CreationNodeWithNonces> {
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
      if (!child || !child.address) {
        throw new Error("child address is undefined");
      }
      const childAddress = Address(getNetwork(network)).decode(child.address);
      const childPkScript = OutScript.encode(childAddress);
      rootNodeTx.addOutput({
        script: childPkScript,
        amount: parentTxOutput.amount / 2n,
      });
    }

    // Add ephemeral anchor output
    const anchor = this.ephemeralAnchorOutput();
    rootNodeTx.addOutput(anchor);

    const rootNodeSigningCommitment =
      await this.config.signer.getRandomSigningCommitment();
    const rootNodeSigningJob: SigningJob = {
      signingPublicKey: root.signingPublicKey,
      rawTx: rootNodeTx.toBytes(),
      signingNonceCommitment: rootNodeSigningCommitment,
    };
    const rootCreationNode: CreationNodeWithNonces = {
      nodeTxSigningJob: rootNodeSigningJob,
      refundTxSigningJob: undefined,
      children: [],
    };
    rootCreationNode.nodeTxSigningCommitment = rootNodeSigningCommitment;

    const leftChild = root.children[0];
    const rightChild = root.children[1];
    if (!leftChild || !rightChild) {
      throw new Error("Root children are undefined");
    }

    const leftChildCreationNode = await this.buildChildCreationNode(
      leftChild,
      rootNodeTx,
      0,
      network
    );
    const rightChildCreationNode = await this.buildChildCreationNode(
      rightChild,
      rootNodeTx,
      1,
      network
    );

    rootCreationNode.children.push(leftChildCreationNode);
    rootCreationNode.children.push(rightChildCreationNode);

    return rootCreationNode;
  }

  private async signNodeCreation(
    parentTx: Transaction,
    vout: number,
    internalNode: DepositAddressTree,
    creationNode: CreationNodeWithNonces,
    creationResponseNode: CreationResponseNode
  ): Promise<{ tx: Transaction; signature: NodeSignatures }> {
    if (
      !creationNode.nodeTxSigningJob?.signingPublicKey ||
      !internalNode.verificationKey
    ) {
      throw new Error("signingPublicKey or verificationKey is undefined");
    }

    const parentTxOutput = parentTx.getOutput(vout);
    if (!parentTxOutput) {
      throw new Error("parentTxOutput is undefined");
    }

    const tx = getTxFromRawTxBytes(creationNode.nodeTxSigningJob.rawTx);
    const txSighash = getSigHashFromTx(tx, 0, parentTxOutput);

    let nodeTxSignature: Uint8Array = new Uint8Array();
    if (creationNode.nodeTxSigningCommitment) {
      const userSignature = await this.config.signer.signFrost({
        message: txSighash,
        publicKey: creationNode.nodeTxSigningJob.signingPublicKey,
        privateAsPubKey: internalNode.signingPublicKey,
        selfCommitment: creationNode.nodeTxSigningCommitment,
        statechainCommitments:
          creationResponseNode.nodeTxSigningResult?.signingNonceCommitments,
        verifyingKey: internalNode.verificationKey,
      });

      nodeTxSignature = await this.config.signer.aggregateFrost({
        message: txSighash,
        statechainSignatures:
          creationResponseNode.nodeTxSigningResult?.signatureShares,
        statechainPublicKeys:
          creationResponseNode.nodeTxSigningResult?.publicKeys,
        verifyingKey: internalNode.verificationKey,
        statechainCommitments:
          creationResponseNode.nodeTxSigningResult?.signingNonceCommitments,
        selfCommitment: creationNode.nodeTxSigningCommitment,
        selfSignature: userSignature,
        publicKey: internalNode.signingPublicKey,
      });
    }

    let refundTxSignature: Uint8Array = new Uint8Array();
    if (creationNode.refundTxSigningCommitment) {
      const rawTx = creationNode.refundTxSigningJob?.rawTx;
      if (!rawTx) {
        throw new Error("rawTx is undefined");
      }
      if (!creationNode.refundTxSigningJob?.signingPublicKey) {
        throw new Error("signingPublicKey is undefined");
      }
      const refundTx = getTxFromRawTxBytes(rawTx);
      const refundTxSighash = getSigHashFromTx(refundTx, 0, parentTxOutput);

      const refundSigningResponse = await this.config.signer.signFrost({
        message: refundTxSighash,
        publicKey: creationNode.refundTxSigningJob.signingPublicKey,
        privateAsPubKey: internalNode.signingPublicKey,
        selfCommitment: creationNode.refundTxSigningCommitment,
        statechainCommitments:
          creationResponseNode.refundTxSigningResult?.signingNonceCommitments,
        verifyingKey: internalNode.verificationKey,
      });

      refundTxSignature = await this.config.signer.aggregateFrost({
        message: refundTxSighash,
        statechainSignatures:
          creationResponseNode.refundTxSigningResult?.signatureShares,
        statechainPublicKeys:
          creationResponseNode.refundTxSigningResult?.publicKeys,
        verifyingKey: internalNode.verificationKey,
        statechainCommitments:
          creationResponseNode.refundTxSigningResult?.signingNonceCommitments,
        selfCommitment: creationNode.refundTxSigningCommitment,
        selfSignature: refundSigningResponse,
        publicKey: internalNode.signingPublicKey,
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
    const rootSignature = await this.signNodeCreation(
      tx,
      vout,
      root,
      rootCreationNode,
      creationResultTreeRoot
    );

    const firstRootChild = root.children[0];
    const secondRootChild = root.children[1];
    const firstRootChildCreationNode = rootCreationNode.children[0];
    const secondRootChildCreationNode = rootCreationNode.children[1];
    const firstRootChildCreationResult = creationResultTreeRoot.children[0];
    const secondRootChildCreationResult = creationResultTreeRoot.children[1];
    if (!firstRootChild || !secondRootChild) {
      throw new Error("Root children are undefined");
    }

    if (!firstRootChildCreationNode || !secondRootChildCreationNode) {
      throw new Error("Root child creation nodes are undefined");
    }

    if (!firstRootChildCreationResult || !secondRootChildCreationResult) {
      throw new Error("Root child creation results are undefined");
    }

    const leftChildSignature = await this.signNodeCreation(
      rootSignature.tx,
      0,
      firstRootChild,
      firstRootChildCreationNode,
      firstRootChildCreationResult
    );

    const rightChildSignature = await this.signNodeCreation(
      rootSignature.tx,
      1,
      secondRootChild,
      secondRootChildCreationNode,
      secondRootChildCreationResult
    );

    const signatures = [
      rootSignature.signature,
      leftChildSignature.signature,
      rightChildSignature.signature,
    ];

    return signatures;
  }
}
