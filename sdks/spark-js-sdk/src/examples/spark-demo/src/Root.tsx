import { BrowserRouter as Router, Route, Routes } from "react-router-dom";
import Login from "./pages/login/Login";
import Receive from "./pages/receive/Receive";
import Wallet from "./pages/wallet/Wallet";
import WalletSuccess from "./pages/wallet-success/WalletSuccess";
import ReceiveDetails from "./components/ReceiveDetails";

export default function Root() {
  return (
    <Router>
      <div className="mt-[40px]">
        <Routes>
          <Route path="/" element={<Login />} />
          <Route path="/wallet-success" element={<WalletSuccess />} />
          <Route path="/wallet" element={<Wallet />} />
          <Route path="/receive" element={<Receive />} />
          <Route path="/send" element={<Receive />} />
          <Route path="/receive-details" element={<ReceiveDetails />} />
        </Routes>
      </div>
    </Router>
  );
}
