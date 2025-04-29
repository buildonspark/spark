
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved





interface RequestLightningSendInput {


    encodedInvoice: string;

    idempotencyKey: string;




}

export const RequestLightningSendInputFromJson = (obj: any): RequestLightningSendInput => {
    return {
        encodedInvoice: obj["request_lightning_send_input_encoded_invoice"],
        idempotencyKey: obj["request_lightning_send_input_idempotency_key"],

        } as RequestLightningSendInput;

}
export const RequestLightningSendInputToJson = (obj: RequestLightningSendInput): any => {
return {
request_lightning_send_input_encoded_invoice: obj.encodedInvoice,
request_lightning_send_input_idempotency_key: obj.idempotencyKey,

        }

}





export default RequestLightningSendInput;
