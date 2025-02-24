/** This is an object representing information about a page returned by the Lightspark API. For more information, please see the “Pagination” section of our API docs for more information about its usage. **/
interface PageInfo {
    hasNextPage?: boolean | undefined;
    hasPreviousPage?: boolean | undefined;
    startCursor?: string | undefined;
    endCursor?: string | undefined;
}
export declare const PageInfoFromJson: (obj: any) => PageInfo;
export declare const PageInfoToJson: (obj: PageInfo) => any;
export declare const FRAGMENT = "\nfragment PageInfoFragment on PageInfo {\n    __typename\n    page_info_has_next_page: has_next_page\n    page_info_has_previous_page: has_previous_page\n    page_info_start_cursor: start_cursor\n    page_info_end_cursor: end_cursor\n}";
export default PageInfo;
