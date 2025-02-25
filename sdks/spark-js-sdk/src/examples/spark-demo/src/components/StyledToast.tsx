import { ToastContainer } from "react-toastify";

export default function StyledToast() {
  return (
    <ToastContainer
      aria-label="toast-container"
      position="bottom-center"
      toastStyle={{
        background: "#0E3154",
        borderRadius: "8px",
        color: "#fff",
      }}
      closeButton={false}
      hideProgressBar
      autoClose={500}
      limit={2}
    />
  );
}
