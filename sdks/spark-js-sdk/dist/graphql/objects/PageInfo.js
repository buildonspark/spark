// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
export const PageInfoFromJson = (obj) => {
    return {
        hasNextPage: obj["page_info_has_next_page"],
        hasPreviousPage: obj["page_info_has_previous_page"],
        startCursor: obj["page_info_start_cursor"],
        endCursor: obj["page_info_end_cursor"],
    };
};
export const PageInfoToJson = (obj) => {
    return {
        page_info_has_next_page: obj.hasNextPage,
        page_info_has_previous_page: obj.hasPreviousPage,
        page_info_start_cursor: obj.startCursor,
        page_info_end_cursor: obj.endCursor,
    };
};
export const FRAGMENT = `
fragment PageInfoFragment on PageInfo {
    __typename
    page_info_has_next_page: has_next_page
    page_info_has_previous_page: has_previous_page
    page_info_start_cursor: start_cursor
    page_info_end_cursor: end_cursor
}`;
//# sourceMappingURL=PageInfo.js.map