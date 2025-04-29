
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved




/** This is an object representing information about a page returned by the Lightspark API. For more information, please see the “Pagination” section of our API docs for more information about its usage. **/
interface PageInfo {


    hasNextPage?: boolean | undefined;

    hasPreviousPage?: boolean | undefined;

    startCursor?: string | undefined;

    endCursor?: string | undefined;




}

export const PageInfoFromJson = (obj: any): PageInfo => {
    return {
        hasNextPage: obj["page_info_has_next_page"],
        hasPreviousPage: obj["page_info_has_previous_page"],
        startCursor: obj["page_info_start_cursor"],
        endCursor: obj["page_info_end_cursor"],

        } as PageInfo;

}
export const PageInfoToJson = (obj: PageInfo): any => {
return {
page_info_has_next_page: obj.hasNextPage,
page_info_has_previous_page: obj.hasPreviousPage,
page_info_start_cursor: obj.startCursor,
page_info_end_cursor: obj.endCursor,

        }

}


    export const FRAGMENT = `
fragment PageInfoFragment on PageInfo {
    __typename
    page_info_has_next_page: has_next_page
    page_info_has_previous_page: has_previous_page
    page_info_start_cursor: start_cursor
    page_info_end_cursor: end_cursor
}`;




export default PageInfo;
