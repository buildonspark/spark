import {
  BrowserRouter as Router,
  Route,
  Routes as RoutesProvider,
} from "react-router-dom";
import SparkWithTextLogo from "./icons/SparkWithTextLogo";
import Login from "./pages/login/Login";
import Receive from "./pages/receive/Receive";
import RecoverWallet from "./pages/recover-wallet/RecoverWallet";
import Send from "./pages/send/Send";
import Tokens from "./pages/tokens/Tokens";
import WalletSuccess from "./pages/wallet-success/WalletSuccess";
import Wallet from "./pages/wallet/Wallet";
import { Routes } from "./routes";
export default function Root() {
  return (
    <Router>
      <div className="flex justify-center p-8">
        <SparkWithTextLogo />
      </div>
      <div className="mt-[40px] min-h-[480px] rounded-3xl rounded-[32px] border border-[0.5px] border-[rgba(249,249,249,0.15)] p-8">
        <RoutesProvider>
          <Route path={Routes.Base} element={<Login />} />
          <Route path={Routes.WalletSuccess} element={<WalletSuccess />} />
          <Route path={Routes.Wallet} element={<Wallet />} />
          <Route path={Routes.Receive} element={<Receive />} />
          <Route path={Routes.Send} element={<Send />} />
          <Route path={Routes.RecoverWallet} element={<RecoverWallet />} />
          <Route path={Routes.Tokens} element={<Tokens />} />
        </RoutesProvider>
      </div>
    </Router>
  );
}
