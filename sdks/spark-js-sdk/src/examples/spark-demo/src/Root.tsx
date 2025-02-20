import Login from "./pages/login/Login";
import Receive from "./pages/receive/Receive";
import Wallet from "./pages/wallet/Wallet";

export default function Root() {
  return (
    <div className="mt-[40px]">
      <Login />
      <Wallet />
      <Receive />
    </div>
  );
}
