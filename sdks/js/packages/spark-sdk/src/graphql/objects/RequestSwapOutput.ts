// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved

interface RequestSwapOutput {
  requestId: string;
}

export const RequestSwapOutputFromJson = (obj: any): RequestSwapOutput => {
  return {
    requestId: obj["request_swap_output_request"].id,
  } as RequestSwapOutput;
};
export const RequestSwapOutputToJson = (obj: RequestSwapOutput): any => {
  return {
    request_swap_output_request: { id: obj.requestId },
  };
};

export const FRAGMENT = `
fragment RequestSwapOutputFragment on RequestSwapOutput {
    __typename
    request_swap_output_request: request {
        id
    }
}`;

export default RequestSwapOutput;
