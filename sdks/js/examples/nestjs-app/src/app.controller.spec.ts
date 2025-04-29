import { Test, TestingModule } from '@nestjs/testing';
import { AppController } from './app.controller';
import { AppService } from './app.service';

describe('AppController', () => {
  let appController: AppController;

  beforeEach(async () => {
    const app: TestingModule = await Test.createTestingModule({
      controllers: [AppController],
      providers: [AppService],
    }).compile();

    appController = app.get<AppController>(AppController);
  });

  describe('root', () => {
    it('should return "Hello World!"', () => {
      expect(appController.getHello()).toBe('Hello World!');
    });

    it('should create a spark wallet', async () => {
      const response = await appController.createSparkWallet();
      expect(response).toBe("Spark Wallet Identity Public Key: 03b1a0b08d13db8befda7e13f3f8843393723121b2115cf26d02c84ec4f5839c71");
    });

    it('should be able to call wasm function', async () => {
      const response = await appController.testWasm();
      expect(response).toBe("2ed5c588ed2a2999344b4c8d60869bcf02a0aa4f7cf0856fddf189f1ff927cdb");
    });
  });
});
