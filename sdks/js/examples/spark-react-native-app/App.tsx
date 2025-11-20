/**
 * Sample React Native App
 * https://github.com/facebook/react-native
 *
 * @format
 */

import { NavigationContainer } from '@react-navigation/native';
import { createNativeStackNavigator } from '@react-navigation/native-stack';
import { WalletProvider } from './contexts/WalletContext';
import HomeScreen from './screens/HomeScreen';
import TestScreen from './screens/TestScreen';
import WalletDetails from './screens/WalletDetails';

export type RootStackParamList = {
  Home: undefined;
  WalletDetails: undefined;
  TestScreen: undefined;
};
const Stack = createNativeStackNavigator<RootStackParamList>();

export default function App() {
  return (
    <WalletProvider>
      <NavigationContainer>
        <Stack.Navigator>
          <Stack.Screen
            name="Home"
            component={HomeScreen}
            options={{ title: 'Connect Wallet' }}
          />
          <Stack.Screen
            name="WalletDetails"
            component={WalletDetails}
            options={{ title: 'My Wallet' }}
          />
          <Stack.Screen
            name="TestScreen"
            component={TestScreen}
            options={{ title: 'Test Screen' }}
          />
        </Stack.Navigator>
      </NavigationContainer>
    </WalletProvider>
  );
}
