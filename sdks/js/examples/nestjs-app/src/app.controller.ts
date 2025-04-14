import { Controller, Get } from '@nestjs/common';
import { AppService } from './app.service';

@Controller()
export class AppController {
  constructor(private readonly appService: AppService) {}

  @Get()
  getHello(): string {
    return this.appService.getHello();
  }

  @Get("create-spark-wallet")
  async createSparkWallet(): Promise<string> {
    const response = await this.appService.createSparkWallet();
    return response;
  }

  @Get("test-wasm")
  async testWasm(): Promise<string> {
    const response = await this.appService.testWasm();
    return response;
  }
}
