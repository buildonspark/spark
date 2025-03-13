import express from "express";
import sparkRoutes from "../routes/sparkRoutes.js";
import issuerRoutes from "../routes/issuerRoutes.js";

const app = express();
app.use(express.json());
// parse bigint to string
app.use((req, res, next) => {
  res.json = function (data) {
    return res.send(
      JSON.stringify(data, (key, value) =>
        typeof value === "bigint" ? value.toString() : value
      )
    );
  };
  next();
});

app.use("/spark-wallet", sparkRoutes);
app.use("/issuer-wallet", issuerRoutes);

app.get("/", (req, res) => {
  res.send("Hello World");
});

const startPort = 5000;
const maxPort = 5010;

function startServer(port) {
  if (port > maxPort) {
    console.error("No available ports found in range");
    process.exit(1);
    return;
  }
  const server = app
    .listen(port)
    .on("listening", () => {
      console.log(`Spark API running on port ${port}`);
    })
    .on("error", (err) => {
      if (err.code === "EADDRINUSE") {
        console.log(`Port ${port} is busy, trying ${port + 1}...`);
        startServer(port + 1);
      } else {
        console.error("Server error:", err);
      }
    });
}

startServer(startPort);
