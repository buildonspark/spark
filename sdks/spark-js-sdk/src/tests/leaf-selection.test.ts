import { LeafNode, selectLeaves } from "../utils/leaf-selection";

function createLeafNode(value: number): LeafNode {
  return {
    id: `leaf_${value}`,
    value: value,
    treeId: "1",
    nodeTx: new Uint8Array(),
    refundTx: new Uint8Array(),
    vout: 0,
    verifyingPublicKey: new Uint8Array(),
    ownerIdentityPublicKey: new Uint8Array(),
    refundTimelock: 0,
    isUsed: false,
  };
}

describe("leaf selection", () => {
  // When selecting leaves, we want to select the smallest leaf that
  // is greater than or equal to the target amount
  it("should select the smallest leaf that is greater than or equal to the target amount", () => {
    const leaves: LeafNode[] = [
      createLeafNode(5000),
      createLeafNode(4000),
      createLeafNode(6000),
      createLeafNode(7000),
    ];

    const selectedLeaves = selectLeaves(leaves, 3500);

    expect(selectedLeaves.length).toBe(1);
    expect(selectedLeaves.reduce((acc, leaf) => acc + leaf.value, 0)).toBe(
      4000
    );
    expect(selectedLeaves[0].isUsed).toBe(true);

    leaves.map((leaf) => {
      if (leaf.id !== selectedLeaves[0].id) {
        expect(leaf.isUsed).toBe(false);
      }
    });
  });

  // When we have to select multiple leaves, we want to select the largest leaf
  // first and then the smallest leaf that completes the target amount
  it("should select the largest leaf and then the smallest leaf that completes the target amount", () => {
    const leaves: LeafNode[] = [
      createLeafNode(2000),
      createLeafNode(1800),
      createLeafNode(1000),
      createLeafNode(1500),
    ];

    const selectedLeaves = selectLeaves(leaves, 3000);

    expect(selectedLeaves.length).toBe(2);
    expect(selectedLeaves.reduce((acc, leaf) => acc + leaf.value, 0)).toBe(
      3000
    );
  });

  // Another test for selecting multiple leaves
  it("should select leaves that sum to the target amount", () => {
    const leaves: LeafNode[] = [
      createLeafNode(1500),
      createLeafNode(1000),
      createLeafNode(500),
    ];

    const selectedLeaves = selectLeaves(leaves, 2500);

    expect(selectedLeaves.length).toBe(2);
    expect(selectedLeaves.reduce((acc, leaf) => acc + leaf.value, 0)).toBe(
      2500
    );

    expect(selectedLeaves[0].value).toBe(1500);
    expect(selectedLeaves[1].value).toBe(1000);
  });

  it("should select leaves that sum to the target amount", () => {
    const leaves: LeafNode[] = [
      createLeafNode(2000),
      createLeafNode(1500),
      createLeafNode(1000),
      createLeafNode(500),
      createLeafNode(1000),
      createLeafNode(5000),
      createLeafNode(10000),
    ];

    const selectedLeaves = selectLeaves(leaves, 20134);

    expect(selectedLeaves.length).toBe(6);
    expect(selectedLeaves.reduce((acc, leaf) => acc + leaf.value, 0)).toBe(
      20500
    );
    expect(selectedLeaves[0].value).toBe(10000);
    expect(selectedLeaves[1].value).toBe(5000);
    expect(selectedLeaves[2].value).toBe(2000);
    expect(selectedLeaves[3].value).toBe(1500);
    expect(selectedLeaves[4].value).toBe(1000);
    expect(selectedLeaves[5].value).toBe(1000);

    leaves.map((leaf) => {
      if (selectedLeaves.includes(leaf)) {
        expect(leaf.isUsed).toBe(true);
      } else {
        expect(leaf.isUsed).toBe(false);
      }
    });
  });

  // When the target amount is greater than the sum of all leaves, we want to throw an error
  it("should throw an error if the target amount is greater than the sum of all leaves", () => {
    const leaves: LeafNode[] = [createLeafNode(500), createLeafNode(300)];

    try {
      selectLeaves(leaves, 1000);
    } catch (e: any) {
      expect(e.message).toBe(
        "Target amount is too large to be processed: requested 1000 but only 800 is available"
      );
    }

    leaves.map((leaf) => {
      expect(leaf.isUsed).toBe(false);
    });
  });

  // When the target amount is less than the minimum value, we want to throw an error
  it("should throw an error if the target amount is less than the minimum value", () => {
    const leaves: LeafNode[] = [createLeafNode(500), createLeafNode(300)];

    try {
      selectLeaves(leaves, 0);
    } catch (e: any) {
      expect(e.message).toBe(
        "Target amount is too small to be processed: requested 0 but minimum is 540"
      );
    }
  });
});
