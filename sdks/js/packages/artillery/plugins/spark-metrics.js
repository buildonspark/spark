/**
 * Artillery Processor for Spark Engine Custom Metrics
 * 
 * This processor captures custom metrics emitted by the Spark engine
 * and ensures they appear in Artillery reports.
 */

module.exports = {
  reportMintMetrics: reportMintMetrics,
  reportTransferMetrics: reportTransferMetrics
};

// Report mint token metrics from context
function reportMintMetrics(req, res, context, events, done) {
  if (context.vars && context.vars.lastMintTxId && context.vars.mintTime) {
    const mintTime = context.vars.mintTime;
    const mintAmount = context.vars.lastMintAmount || 0;
    
    console.log(`[SparkMetrics] Reporting mint metrics: time=${mintTime}ms, amount=${mintAmount}`);
    
    // Emit metrics to Artillery
    events.emit('histogram', 'spark.mint_token_time', mintTime);
    events.emit('counter', 'spark.tokens_minted', 1);
    events.emit('counter', 'spark.tokens_minted_amount', mintAmount);
  }
  
  return done();
}

// Report transfer token metrics from context  
function reportTransferMetrics(req, res, context, events, done) {
  if (context.vars && context.vars.transferTime) {
    const transferTime = context.vars.transferTime;
    const transferAmount = context.vars.transferAmount || 0;
    
    console.log(`[SparkMetrics] Reporting transfer metrics: time=${transferTime}ms, amount=${transferAmount}`);
    
    // Emit metrics to Artillery
    events.emit('histogram', 'spark.token_transfer_time', transferTime);
    events.emit('counter', 'spark.token_transfer_success', 1);
    events.emit('counter', 'spark.tokens_transferred', transferAmount);
  }
  
  return done();
}