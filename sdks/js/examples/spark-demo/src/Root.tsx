import {
  Route,
  BrowserRouter as Router,
  Routes as RoutesProvider,
  useLocation,
} from "react-router-dom";
import StyledToast from "./components/StyledToast";
import SparkWithTextLogo from "./icons/SparkWithTextLogo";
import Login from "./pages/login/Login";
import Receive from "./pages/receive/Receive";
import Send from "./pages/send/Send";
import Tokens from "./pages/tokens/Tokens";
import Transactions from "./pages/transactions/Transactions";
import Wallet from "./pages/wallet/Wallet";
import { Routes } from "./routes";
export default function Root() {
  return (
    <Router>
      <AppContent />
    </Router>
  );
}

const AppContent = () => {
  const location = useLocation();
  const isLoginPage =
    location.pathname === Routes.Base || location.pathname === "/";

  return (
    <>
      <div className="flex h-9xl justify-center p-xl">
        {!isLoginPage && <SparkWithTextLogo />}
      </div>
      <div
        className={`card-background-gradient min-h-[480px] rounded-xl`}
        style={{
          padding: "0.5px",
        }}
      >
        <div className="h-full min-h-[480px] rounded-xl bg-[#0A0A0A] xs:p-8">
          <RoutesProvider>
            <Route path={Routes.Base} element={<Login />} />
            <Route path={Routes.Wallet} element={<Wallet />} />
            <Route path={Routes.Receive} element={<Receive />} />
            <Route path={Routes.Send} element={<Send />} />
            <Route path={Routes.Tokens} element={<Tokens />} />
            <Route path={Routes.Transactions} element={<Transactions />} />
          </RoutesProvider>
          <StyledToast />
        </div>
      </div>
    </>
  );
};
