-- Will take less than 30secs to execute in prod
UPDATE deposit_addresses
SET network = CASE
     WHEN address LIKE 'bc1%' THEN 'MAINNET'
     WHEN address LIKE 'bcrt1%' THEN 'REGTEST'
     ELSE NULL
END WHERE network is null;

ALTER TABLE "deposit_addresses" ALTER COLUMN "network" DROP NOT NULL;
