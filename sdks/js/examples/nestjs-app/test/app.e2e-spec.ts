import { Test, TestingModule } from "@nestjs/testing";
import { INestApplication } from "@nestjs/common";
import * as request from "supertest";
import { AppModule } from "./../src/app.module";

describe("AppController (e2e)", () => {
  let app: INestApplication;

  beforeEach(async () => {
    const moduleFixture: TestingModule = await Test.createTestingModule({
      imports: [AppModule],
    }).compile();

    app = moduleFixture.createNestApplication();
    await app.init();
  });

  afterAll((done) => {
    app.close();
    done();
  });

  it("/ (GET)", () => {
    return request(app.getHttpServer())
      .get("/")
      .expect(200)
      .expect("Hello World!");
  });

  it("/create-spark-wallet (GET)", () => {
    return request(app.getHttpServer())
      .get("/create-spark-wallet")
      .expect(200)
      .expect(
        "Spark Wallet Identity Public Key: 02a81f113befc188d45030511e5a4c9d9f83a515b0f594d3f8d53b1989109d85d5",
      );
  });

  it("/test-wasm (GET)", () => {
    return request(app.getHttpServer())
      .get("/test-wasm")
      .expect(200)
      .expect(
        "ddbf1a5b95c00e211344b1212ea3a836d7ddf6a64f9cdb0b08c5e50035dab33b",
      );
  });
});
