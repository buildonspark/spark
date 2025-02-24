interface RequestLightningSendInput {
    encodedInvoice: string;
    idempotencyKey: string;
}
export declare const RequestLightningSendInputFromJson: (obj: any) => RequestLightningSendInput;
export declare const RequestLightningSendInputToJson: (obj: RequestLightningSendInput) => any;
export default RequestLightningSendInput;
