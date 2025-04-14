import { Test, TestingModule } from '@nestjs/testing';
import { INestApplication } from '@nestjs/common';
import * as request from 'supertest';
import { AppModule } from './../src/app.module';

describe('AppController (e2e)', () => {
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

  it('/ (GET)', () => {
    return request(app.getHttpServer())
      .get('/')
      .expect(200)
      .expect('Hello World!');
  });

  it('/create-spark-wallet (GET)', () => {
    return request(app.getHttpServer())
      .get('/create-spark-wallet')
      .expect(200)
      .expect("Spark Wallet Identity Public Key: 03b1a0b08d13db8befda7e13f3f8843393723121b2115cf26d02c84ec4f5839c71");
  });

  it('/test-wasm (GET)', () => {
    return request(app.getHttpServer())
      .get('/test-wasm')
      .expect(200)
      .expect("2ed5c588ed2a2999344b4c8d60869bcf02a0aa4f7cf0856fddf189f1ff927cdb");
  });
});
