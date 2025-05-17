
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved

import Connection from './Connection.js';
import {UserRequestFromJson} from './UserRequest.js';
import UserRequest from './UserRequest.js';
import {PageInfoFromJson} from './PageInfo.js';
import {UserRequestToJson} from './UserRequest.js';
import PageInfo from './PageInfo.js';
import {PageInfoToJson} from './PageInfo.js';


interface SparkWalletUserToUserRequestsConnection {


    /**
 * The total count of objects in this connection, using the current filters. It is different from the
 * number of objects returned in the current page (in the `entities` field).
**/
count: number;

    /** An object that holds pagination information about the objects in this connection. **/
pageInfo: PageInfo;

    /** The Spark User Requests for the current page of this connection. **/
entities: UserRequest[];

    /** The typename of the object **/
typename: string;




}

export const SparkWalletUserToUserRequestsConnectionFromJson = (obj: any): SparkWalletUserToUserRequestsConnection => {
    return {
        count: obj["spark_wallet_user_to_user_requests_connection_count"],
        pageInfo: PageInfoFromJson(obj["spark_wallet_user_to_user_requests_connection_page_info"]),
        entities: obj["spark_wallet_user_to_user_requests_connection_entities"].map((e) => UserRequestFromJson(e)),
typename: "SparkWalletUserToUserRequestsConnection",
        } as SparkWalletUserToUserRequestsConnection;

}
export const SparkWalletUserToUserRequestsConnectionToJson = (obj: SparkWalletUserToUserRequestsConnection): any => {
return {
__typename: "SparkWalletUserToUserRequestsConnection",spark_wallet_user_to_user_requests_connection_count: obj.count,
spark_wallet_user_to_user_requests_connection_page_info: PageInfoToJson(obj.pageInfo),
spark_wallet_user_to_user_requests_connection_entities: obj.entities.map((e) => UserRequestToJson(e)),

        }

}


    export const FRAGMENT = `
fragment SparkWalletUserToUserRequestsConnectionFragment on SparkWalletUserToUserRequestsConnection {
    __typename
    spark_wallet_user_to_user_requests_connection_count: count
    spark_wallet_user_to_user_requests_connection_page_info: page_info {
        __typename
        page_info_has_next_page: has_next_page
        page_info_has_previous_page: has_previous_page
        page_info_start_cursor: start_cursor
        page_info_end_cursor: end_cursor
    }
    spark_wallet_user_to_user_requests_connection_entities: entities {
        id
    }
}`;




export default SparkWalletUserToUserRequestsConnection;
