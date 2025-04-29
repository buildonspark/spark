use lightspark::{
    client::LightsparkClient,
    key::RSASigningKey,
    objects::{bitcoin_network::BitcoinNetwork, currency_unit::CurrencyUnit},
    request::auth_provider::AccountAuthProvider,
};

#[tokio::main]
async fn main() {
    let args: Vec<String> = std::env::args().collect();
    if args.len() < 2 {
        println!("Usage: paylightning <invoice>");
        return;
    }
    let invoice = args[1].clone();

    let api_id = "019538e24e664f890000c09f302503e6".to_owned();
    let api_token = "QgA8Lo5HzIkYfaD3Itjxdlgriu1nnohRDBOWAMolzho".to_owned();
    let dev_url = "https://api.dev.dev.sparkinfra.net/graphql/server/2023-09-13".to_owned();

    let auth_provider = AccountAuthProvider::new(api_id, api_token);
    let mut client = match LightsparkClient::<RSASigningKey>::new(auth_provider) {
        Ok(value) => value,
        Err(err) => {
            println!("{}", err);
            return;
        }
    };

    client.requester.set_base_url(Some(dev_url));
    let node_id = "LightsparkNodeWithOSKLND:01951b13-406f-f96b-0000-63fa81180f42";
    let node_password = "1234!@#$";
    let _ = match client
        .recover_node_signing_key(node_id, node_password)
        .await
    {
        Ok(v) => v,
        Err(err) => {
            println!("{}", err);
            return;
        }
    };

    let decoded_request = match client.get_decoded_payment_request(invoice.as_str()).await {
        Ok(v) => v,
        Err(err) => {
            println!("{}", err);
            return;
        }
    };

    let amount_sats = match decoded_request.amount.original_unit {
        CurrencyUnit::Satoshi => decoded_request.amount.original_value,
        CurrencyUnit::Bitcoin => decoded_request.amount.original_value * 100000000,
        CurrencyUnit::Millisatoshi => decoded_request.amount.original_value / 1000,
        _ => decoded_request.amount.original_value,
    };

    if amount_sats > 10000 {
        println!("Amount is greater than 10000 sats");
        return;
    }

    let account = match client.get_current_account().await {
        Ok(v) => v,
        Err(err) => {
            println!("{}", err);
            return;
        }
    };

    let local_balance = account
        .get_local_balance(&client.requester, Some(vec![BitcoinNetwork::Regtest]), None)
        .await;
    if let Ok(Some(local_balance)) = local_balance {
        if local_balance.original_value / 1000 < 100000 {
            match client.fund_node(node_id, 100000).await {
                Ok(_) => {}
                Err(err) => {
                    println!("{}", err);
                }
            }
        }
    }

    let payment = match client
        .pay_invoice(&node_id, invoice.as_str(), 60, None, 100000)
        .await
    {
        Ok(v) => v,
        Err(err) => {
            println!("{}", err);
            return;
        }
    };

    println!("Payment sent: {:?}", payment.id);
}
