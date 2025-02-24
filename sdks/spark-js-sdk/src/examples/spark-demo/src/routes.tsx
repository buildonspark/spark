export const Routes = {
  Current: "",
  Base: "/",

  // App routes
  Login: "/login",
  WalletSuccess: "/wallet-success",
  Wallet: "/wallet",
  Receive: "/receive",
  Send: "/send",
  RecoverWallet: "/recover-wallet",
  Tokens: "/tokens",
};

export type RoutesMapType = typeof Routes;
export type RoutesType = (typeof Routes)[keyof typeof Routes];
export type RouteNames = keyof typeof Routes;

/* Query params that are appended to path routes: */
export type RouteQueryParams = {
  [K in RouteNames]: Record<string, string>;
};

export const routeQueryParams: RouteQueryParams = {
  // Avoid needing optional chaining on query params:
  ...Object.keys(Routes).reduce((acc, key) => {
    acc[key as RouteNames] = {};
    return acc;
  }, {} as RouteQueryParams),
};

/* Query params that function as routes: */
export const QueryParamRouteKeys = {
  AddFunds: "add-funds",
};
export const QueryParamRoutes = {
  AddFunds: {
    to: Routes.Current,
    params: { query: { [QueryParamRouteKeys.AddFunds]: "" } },
  },
};

// type D = NewRoutes["APIDocsPage"];
// type E = NewRoutes["home"];

/* Keep these - see LIG-5374 */
declare global {
  interface NewRoutes extends RoutesMapType {}
}
